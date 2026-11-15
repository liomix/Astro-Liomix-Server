package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/config"
)

// adminCommandTimeout bounds a CLI run: connecting, migrating and deriving one
// password. It is longer than a request because a first run on a cold database
// has migrations to apply.
const adminCommandTimeout = 30 * time.Second

// runAdmin dispatches `server admin …`.
//
// Accounts live in the admin_accounts table; this command is how the first row
// gets there. It exists so a deployment does not have to keep a password in the
// environment just to create one account: the operator runs it once, the
// password never leaves the terminal, and the row is written with a PBKDF2
// digest like every other account.
func runAdmin(args []string) error {
	if len(args) == 0 {
		adminUsage()
		return nil
	}
	switch args[0] {
	case "create":
		return adminCreate(args[1:])
	case "passwd":
		return adminPasswd(args[1:])
	case "list":
		return adminList(args[1:])
	default:
		adminUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func adminUsage() {
	fmt.Fprint(os.Stderr, `用法:
  server admin create -username <name> [-display-name <name>] [-password-stdin]
  server admin passwd -username <name> [-password-stdin]
  server admin list

说明:
  create  创建管理员账号。默认生成一个 24 位随机密码并打印一次；
          -password-stdin 从标准输入读取自选密码（推荐用管道，避免进 shell 历史）。
  passwd  重置密码，并撤销该账号的全部会话。
  list    列出账号（id、用户名、状态、最近登录）。

密码不通过命令行参数传入：进程参数会出现在 shell 历史与 ps 输出里。
`)
}

// adminCreate adds the first (or another) administrator account.
func adminCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	username := fs.String("username", "", "账号用户名（小写字母、数字、. _ -，3–40 位）")
	displayName := fs.String("display-name", "", "显示名，留空则与用户名相同")
	fromStdin := fs.Bool("password-stdin", false, "从标准输入读取密码，而不是自动生成")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return errors.New("create: -username 必填")
	}

	return withAdminService(func(ctx context.Context, svc *admin.Service) error {
		password, generated, err := choosePassword(*fromStdin, *username, os.Stdin)
		if err != nil {
			return err
		}
		account, err := svc.CreateAccount(ctx, admin.CreateAccountInput{
			Username:    *username,
			DisplayName: *displayName,
			Password:    password,
		}, audit.Actor{Name: "cli"})
		if err != nil {
			return err
		}

		fmt.Printf("已创建管理员账号 %s（id=%s）\n", account.Username, account.ID)
		if generated {
			fmt.Printf("初始密码：%s\n", password)
			fmt.Println("这是唯一一次显示；请登录 /admin 后立即在「账号」页修改。")
		} else {
			fmt.Println("密码已按标准输入设置。")
		}
		return nil
	})
}

// adminPasswd resets a password from the terminal.
func adminPasswd(args []string) error {
	fs := flag.NewFlagSet("admin passwd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	username := fs.String("username", "", "要重置的账号用户名")
	fromStdin := fs.Bool("password-stdin", false, "从标准输入读取新密码，而不是自动生成")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return errors.New("passwd: -username 必填")
	}

	return withAdminService(func(ctx context.Context, svc *admin.Service) error {
		account, err := svc.GetAccountByUsername(ctx, *username)
		if err != nil {
			return err
		}
		password, generated, err := choosePassword(*fromStdin, account.Username, os.Stdin)
		if err != nil {
			return err
		}
		// The actor has no ID matching the account, which is what tells the
		// service this is an administrator reset and not a self-service change:
		// no current password is required, and every session is revoked.
		if err := svc.ChangePassword(ctx, account.ID, admin.ChangePasswordInput{
			NewPassword: password,
		}, audit.Actor{Name: "cli"}); err != nil {
			return err
		}

		fmt.Printf("已重置 %s 的密码，该账号的会话已全部撤销。\n", account.Username)
		if generated {
			fmt.Printf("新密码：%s\n", password)
			fmt.Println("这是唯一一次显示；请登录 /admin 后立即在「账号」页修改。")
		}
		return nil
	})
}

// adminList prints the accounts so an operator can confirm what the database
// holds without opening psql.
func adminList(args []string) error {
	fs := flag.NewFlagSet("admin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	return withAdminService(func(ctx context.Context, svc *admin.Service) error {
		accounts, _, err := svc.ListAccounts(ctx, admin.AccountQuery{Limit: 200})
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			fmt.Println("还没有管理员账号。用 `server admin create -username <name>` 创建第一个。")
			return nil
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ID\t用户名\t状态\t最近登录\t创建时间")
		for _, account := range accounts {
			lastLogin := "从未"
			if !account.LastLoginAt.IsZero() {
				lastLogin = account.LastLoginAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				account.ID,
				account.Username,
				account.Status,
				lastLogin,
				account.CreatedAt.Local().Format("2006-01-02 15:04"),
			)
		}
		return writer.Flush()
	})
}

// withAdminService runs fn against a service wired to the configured database.
//
// It applies migrations first, exactly like the server does: a CLI that wrote
// to an older schema would produce the kind of breakage that only shows up on
// the next deploy.
func withAdminService(fn func(context.Context, *admin.Service) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()

	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Logs go to stderr at warn level: stdout belongs to the command's own
	// output, and a successful run should not be buried under "database ready".
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc, err := admin.NewService(admin.NewPostgresRepository(db),
		admin.WithLogger(logger),
		admin.WithAuditor(audit.NewStore(db)),
	)
	if err != nil {
		return err
	}
	return fn(ctx, svc)
}

// choosePassword returns the password to store, and whether it was generated.
//
// Generated by default: a password typed into a terminal is echoed, and one
// passed on the command line lands in shell history and in `ps`. A random
// 24-character secret handed over once is safer than either, and the operator
// is told to change it.
func choosePassword(fromStdin bool, username string, stdin io.Reader) (password string, generated bool, err error) {
	if fromStdin {
		reader := bufio.NewReader(stdin)
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", false, fmt.Errorf("read password from stdin: %w", readErr)
		}
		// Only line endings are trimmed: leading or trailing spaces are part of
		// the password the operator chose.
		password = strings.TrimRight(line, "\r\n")
		if err := admin.ValidatePassword("password", username, password); err != nil {
			return "", false, fmt.Errorf("password from stdin: %w", err)
		}
		return password, false, nil
	}

	for attempt := 0; attempt < 5; attempt++ {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			return "", false, fmt.Errorf("generate password: %w", err)
		}
		// 18 bytes encode to 24 URL-safe characters: the policy needs 12, and
		// this carries about 144 bits of entropy.
		candidate := base64.RawURLEncoding.EncodeToString(buf)
		if admin.ValidatePassword("password", username, candidate) == nil {
			return candidate, true, nil
		}
	}
	return "", false, errors.New("generate password: could not satisfy the password policy")
}
