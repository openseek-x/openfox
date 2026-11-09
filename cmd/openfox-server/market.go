package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/market"
	"openfox/internal/platform/config"
)

// marketCommand 处理 `openfox-server market import --dir <目录> [--prune] [--dry-run]`：
// 把兼容的市场数据目录导入市场目录。数据库连接与存储目录沿用 serve 的配置。
// （OPENFOX_DATABASE_URL_FILE、OPENFOX_STORAGE_ROOT）；写入以 openfox_migrator 身份进行。
func marketCommand(args []string) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New("usage: openfox-server market import --dir <directory> [--prune] [--dry-run]")
	}
	flags := flag.NewFlagSet("market import", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dir := flags.String("dir", "", "directory with compatible market export data")
	prune := flags.Bool("prune", false, "delete skills and templates that are not in the data")
	dryRun := flags.Bool("dry-run", false, "parse and compare only; write nothing")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*dir) == "" {
		return errors.New("usage: openfox-server market import --dir <directory> [--prune] [--dry-run]")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if strings.TrimSpace(cfg.Database.URLFile) == "" {
		return errors.New("OPENFOX_DATABASE_URL_FILE is required")
	}
	var storage artifacts.Storage
	if strings.TrimSpace(cfg.Storage.Root) != "" {
		storage = artifacts.Local{Root: cfg.Storage.Root}
	} else if !*dryRun {
		return errors.New("OPENFOX_STORAGE_ROOT is required")
	}
	contents, err := os.ReadFile(cfg.Database.URLFile)
	if err != nil {
		return fmt.Errorf("read database URL file: %w", err)
	}

	started := time.Now()
	data, err := market.LoadDir(*dir)
	if err != nil {
		return fmt.Errorf("read market data: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, strings.TrimSpace(string(contents)))
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer conn.Close(context.Background())
	report, err := market.Import(ctx, conn, storage, data, market.ImportOptions{Source: "builtin", Prune: *prune, DryRun: *dryRun})
	if err != nil {
		return err
	}
	printMarketReport(report, time.Since(started))
	return nil
}

func printMarketReport(report market.Report, elapsed time.Duration) {
	mode := ""
	if report.DryRun {
		mode = " (dry run, nothing written)"
	}
	fmt.Printf("market import%s finished in %s\n", mode, elapsed.Round(time.Millisecond))
	fmt.Printf("skills:    imported=%d updated=%d unchanged=%d deleted=%d skipped=%d\n",
		report.SkillsImported, report.SkillsUpdated, report.SkillsUnchanged, report.SkillsDeleted, len(report.SkippedSkills))
	fmt.Printf("templates: imported=%d updated=%d unchanged=%d deleted=%d skipped=%d\n",
		report.TemplatesImported, report.TemplatesUpdated, report.TemplatesUnchanged, report.TemplatesDeleted, len(report.SkippedTemplates))
	for _, skip := range report.SkippedSkills {
		fmt.Printf("skipped skill %s: %s %s\n", skip.ID, skip.Reason, skip.Detail)
	}
	for _, skip := range report.SkippedTemplates {
		fmt.Printf("skipped template %s: %s %s\n", skip.ID, skip.Reason, skip.Detail)
	}
}
