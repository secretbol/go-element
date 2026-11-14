// Package cmd wires the go-element command line.
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/carlmjohnson/versioninfo"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/secretbol/go-element/internal/infra/matrix"
	"github.com/secretbol/go-element/internal/infra/mcpserver"
)

// Execute builds the command tree and runs it, exiting non-zero on error.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	root := &cli.Command{
		Name:    "go-element",
		Usage:   "MCP server for Matrix/Element rooms, end-to-end encrypted",
		Version: versioninfo.Short(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "dir",
				Usage:   "state directory (session, crypto store)",
				Value:   matrix.DefaultDir(),
				Sources: cli.EnvVars("GO_ELEMENT_DIR"),
			},
		},
		Commands: []*cli.Command{loginCommand(), mcpCommand(), whoamiCommand(), importKeysCommand(), verifyCommand()},
	}

	// MCP speaks on stdout, so every log line goes to stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	err := root.Run(ctx, os.Args)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-element:", err)
		os.Exit(1)
	}
}

func loginCommand() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "log in with a password and create this tool's own device",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "homeserver", Required: true, Sources: cli.EnvVars("GO_ELEMENT_HOMESERVER")},
			&cli.StringFlag{Name: "user", Required: true, Usage: "user id or localpart", Sources: cli.EnvVars("GO_ELEMENT_USER")},
			&cli.StringFlag{Name: "device-name", Value: "go-element", Usage: "name shown under Settings → Sessions"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			password, err := readPassword()
			if err != nil {
				return err
			}
			s, err := matrix.Login(ctx, cmd.String("dir"), cmd.String("homeserver"), cmd.String("user"), password, cmd.String("device-name"))
			if err != nil {
				return fmt.Errorf("login: %w", err)
			}
			fmt.Printf("logged in as %s, device %s\n", s.UserID, s.DeviceID)
			return nil
		},
	}
}

// readPassword takes GO_ELEMENT_PASSWORD, else one line from a piped stdin, else prompts on the terminal.
func readPassword() (string, error) {
	if p := os.Getenv("GO_ELEMENT_PASSWORD"); p != "" {
		return p, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "password: ")
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(raw), nil
}

func importKeysCommand() *cli.Command {
	return &cli.Command{
		Name:  "import-keys",
		Usage: "restore room keys from the server-side backup, using the recovery key (env GO_ELEMENT_RECOVERY_KEY, or one line on stdin)",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			key := os.Getenv("GO_ELEMENT_RECOVERY_KEY")
			if key == "" {
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("reading recovery key from stdin: %w", err)
				}
				key = line
			}
			c, err := matrix.Open(ctx, cmd.String("dir"))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			defer c.Close()
			version, err := c.RestoreFromBackup(ctx, key)
			if err != nil {
				return fmt.Errorf("import keys: %w", err)
			}
			fmt.Printf("restored keys from backup version %s\n", version)
			return nil
		},
	}
}

func verifyCommand() *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "cross-sign this device with the recovery key (env GO_ELEMENT_RECOVERY_KEY, or one line on stdin); --status only reports",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "status", Usage: "report the verification state and change nothing"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := matrix.Open(ctx, cmd.String("dir"))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			defer c.Close()
			if !cmd.Bool("status") {
				key := os.Getenv("GO_ELEMENT_RECOVERY_KEY")
				if key == "" {
					line, err := bufio.NewReader(os.Stdin).ReadString('\n')
					if err != nil && line == "" {
						return fmt.Errorf("reading recovery key from stdin: %w", err)
					}
					key = line
				}
				if err := c.VerifyWithRecoveryKey(ctx, key); err != nil {
					return err
				}
			}
			st, err := c.VerificationStatus(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("%s device %s: cross-signing keys %t, verified %t\n", c.UserID(), c.DeviceID(), st.HasKeys, st.Verified)
			if !st.Verified {
				return errors.New("this device is not verified")
			}
			return nil
		},
	}
}

func whoamiCommand() *cli.Command {
	return &cli.Command{
		Name:  "whoami",
		Usage: "prove the stored session works",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := matrix.Open(ctx, cmd.String("dir"))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			defer c.Close()
			fmt.Printf("%s device %s\n", c.UserID(), c.DeviceID())
			return nil
		},
	}
}

func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "serve the MCP tools over stdio",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := matrix.Open(ctx, cmd.String("dir"))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			defer c.Close()
			slog.Info("serving", "user", c.UserID(), "device", c.DeviceID())
			if err := mcpserver.New(c, versioninfo.Short()).Run(ctx); err != nil {
				return fmt.Errorf("mcp: %w", err)
			}
			return nil
		},
	}
}
