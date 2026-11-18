package main

// 协作 CLI：auth / profile / org / me。令牌只从 stdin 或 OPENFOX_TOKEN 读取，绝不出现在 argv、输出或错误信息里。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"openfox/internal/cliconfig"
)

func (c *cli) runAuth(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox auth <login|status|logout>")
	}
	switch args[0] {
	case "login":
		return ignoreHelp(c.authLogin(ctx, args[1:]))
	case "status":
		return ignoreHelp(c.authStatus(ctx, args[1:]))
	case "logout":
		return ignoreHelp(c.authLogout(args[1:]))
	}
	return usageError("unknown auth command %q", args[0])
}

// authLogin 用个人访问令牌登录：令牌取自 OPENFOX_TOKEN，否则读 stdin；先向中心验证，成功后才写入本地。
func (c *cli) authLogin(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	cf.register(fs, true)
	server := fs.String("server", "", "center URL, e.g. https://openfox.example.com (default: OPENFOX_SERVER)")
	usage := "openfox auth login [--profile <name>] --server <url> [--org <slug|id>]   (token from stdin or OPENFOX_TOKEN)"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: %s", usage)
	}
	name := firstNonEmpty(cf.profile, c.getenv("OPENFOX_PROFILE"), "default")
	if err := cliconfig.ValidateProfileName(name); err != nil {
		return usageError("%v", err)
	}
	target := firstNonEmpty(*server, c.getenv("OPENFOX_SERVER"))
	if target == "" {
		return usageError("--server is required")
	}
	token := strings.TrimSpace(c.getenv("OPENFOX_TOKEN"))
	if token == "" {
		data, err := io.ReadAll(io.LimitReader(c.stdin, 4097))
		if err != nil {
			return wrapClientError(err)
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return usageError("no token provided; pipe it on stdin or set OPENFOX_TOKEN (never pass it as an argument)")
	}
	client, err := c.newClient(target, token)
	if err != nil {
		return usageError("%v", err) // 错误文本不含令牌
	}
	session := &cliSession{cli: c, cf: cf, client: client, profile: name}
	body, err := session.get(ctx, "/v1/me", nil)
	if err != nil {
		return err
	}
	me, err := decodeObject(body)
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return wrapClientError(err)
	}
	profile := file.Profiles[name]
	if profile.Server != client.BaseURL {
		profile.Org = ""
	}
	profile.Server = client.BaseURL
	if cf.org != "" {
		profile.Org = cf.org
	} else if env := c.getenv("OPENFOX_ORG"); env != "" {
		profile.Org = env
	}
	if err := store.WriteToken(name, token); err != nil {
		return wrapClientError(err)
	}
	file.Profiles[name] = profile
	if file.Current == "" {
		file.Current = name
	}
	if err := store.Save(file); err != nil {
		return wrapClientError(err)
	}
	fmt.Fprintf(c.stdout, "Logged in as %s on %s (profile %s).\n", field(me, "email"), client.BaseURL, name)
	return nil
}

func (c *cli) authStatus(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("auth status", flag.ContinueOnError)
	cf.register(fs, true)
	positional, err := c.parse(fs, cf, "openfox auth status", args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: openfox auth status")
	}
	session, err := c.open(ctx, cf, false)
	if err != nil {
		return err
	}
	body, err := session.get(ctx, "/v1/me", nil)
	if err != nil {
		return err
	}
	me, err := decodeObject(body)
	if err != nil {
		return err
	}
	source := "profile file"
	if strings.TrimSpace(c.getenv("OPENFOX_TOKEN")) != "" {
		source = "OPENFOX_TOKEN"
	}
	if cf.output == "json" {
		return c.printJSONValue(map[string]any{
			"profile": session.profile, "server": session.client.BaseURL, "org": session.org, "token_source": source, "user": me,
		})
	}
	c.printPairs([][2]string{
		{"profile", session.profile}, {"server", session.client.BaseURL}, {"organization", session.org},
		{"user", field(me, "email")}, {"user id", field(me, "id")}, {"token", "valid (from " + source + ")"},
	})
	return nil
}

func (c *cli) authLogout(args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("auth logout", flag.ContinueOnError)
	cf.register(fs, false)
	positional, err := c.parse(fs, cf, "openfox auth logout [--profile <name>]", args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: openfox auth logout [--profile <name>]")
	}
	store, err := c.store()
	if err != nil {
		return wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return wrapClientError(err)
	}
	name := firstNonEmpty(cf.profile, c.getenv("OPENFOX_PROFILE"), file.Current)
	if name == "" {
		return usageError("no profile to log out of")
	}
	if err := cliconfig.ValidateProfileName(name); err != nil {
		return usageError("%v", err)
	}
	if err := store.DeleteToken(name); err != nil {
		return wrapClientError(err)
	}
	delete(file.Profiles, name)
	if file.Current == name {
		file.Current = ""
	}
	if err := store.Save(file); err != nil {
		return wrapClientError(err)
	}
	fmt.Fprintf(c.stdout, "Removed the local token and profile %q.\n", name)
	fmt.Fprintln(c.stdout, "The token itself stays valid on the server until you revoke it in the web app (Settings > Personal access tokens).")
	return nil
}

// printJSONValue 输出本地构造的 JSON（不是服务端响应体）。
func (c *cli) printJSONValue(value any) error {
	encoded, err := jsonMarshalIndent(value)
	if err != nil {
		return err
	}
	c.printRaw(encoded)
	return nil
}

func (c *cli) runProfile(_ context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox profile <list|use>")
	}
	switch args[0] {
	case "list":
		return ignoreHelp(c.profileList(args[1:]))
	case "use":
		return ignoreHelp(c.profileUse(args[1:]))
	}
	return usageError("unknown profile command %q", args[0])
}

func (c *cli) profileList(args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
	fs.StringVar(&cf.output, "output", "table", "output format: table or json")
	positional, err := c.parse(fs, cf, "openfox profile list", args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: openfox profile list")
	}
	store, err := c.store()
	if err != nil {
		return wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return wrapClientError(err)
	}
	names := make([]string, 0, len(file.Profiles))
	for name := range file.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	type entry struct {
		Name     string `json:"name"`
		Server   string `json:"server"`
		Org      string `json:"org,omitempty"`
		Current  bool   `json:"current"`
		HasToken bool   `json:"has_token"`
	}
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		_, statErr := os.Lstat(filepath.Join(store.Dir, "tokens", name))
		entries = append(entries, entry{name, file.Profiles[name].Server, file.Profiles[name].Org, name == file.Current, statErr == nil})
	}
	if cf.output == "json" {
		return c.printJSONValue(map[string]any{"current": file.Current, "profiles": entries})
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		mark := ""
		if e.Current {
			mark = "*"
		}
		rows = append(rows, []string{mark, e.Name, e.Server, e.Org, fmt.Sprint(e.HasToken)})
	}
	c.printTable([]string{"CURRENT", "NAME", "SERVER", "ORG", "TOKEN"}, rows)
	return nil
}

func (c *cli) profileUse(args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("profile use", flag.ContinueOnError)
	fs.StringVar(&cf.output, "output", "table", "output format: table or json")
	positional, err := c.parse(fs, cf, "openfox profile use <name>", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox profile use <name>")
	}
	store, err := c.store()
	if err != nil {
		return wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return wrapClientError(err)
	}
	if _, ok := file.Profiles[positional[0]]; !ok {
		return failure(cliExitNotFound, "PROFILE_NOT_FOUND", "profile %q not found", positional[0])
	}
	file.Current = positional[0]
	if err := store.Save(file); err != nil {
		return wrapClientError(err)
	}
	fmt.Fprintf(c.stdout, "Now using profile %q.\n", positional[0])
	return nil
}

func (c *cli) runOrg(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox org <list|use>")
	}
	switch args[0] {
	case "list":
		return ignoreHelp(c.orgList(ctx, args[1:]))
	case "use":
		return ignoreHelp(c.orgUse(ctx, args[1:]))
	}
	return usageError("unknown org command %q", args[0])
}

func (c *cli) orgList(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("org list", flag.ContinueOnError)
	cf.register(fs, true)
	positional, err := c.parse(fs, cf, "openfox org list", args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: openfox org list")
	}
	session, err := c.open(ctx, cf, false)
	if err != nil {
		return err
	}
	body, err := session.get(ctx, "/v1/me/organizations", nil)
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		var rows [][]string
		for _, org := range objectsAt(value, "organizations") {
			mark := ""
			if session.org != "" && (strings.EqualFold(session.org, field(org, "id")) || strings.EqualFold(session.org, field(org, "slug"))) {
				mark = "*"
			}
			rows = append(rows, []string{mark, field(org, "id"), field(org, "slug"), field(org, "name"), field(org, "role")})
		}
		c.printTable([]string{"CURRENT", "ID", "SLUG", "NAME", "ROLE"}, rows)
	})
}

// orgUse 把默认组织写入当前 profile。
func (c *cli) orgUse(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("org use", flag.ContinueOnError)
	cf.register(fs, false)
	positional, err := c.parse(fs, cf, "openfox org use <slug|id>", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox org use <slug|id>")
	}
	session, err := c.open(ctx, cf, false)
	if err != nil {
		return err
	}
	if session.profile == "" {
		return usageError("org use needs a profile; run `openfox auth login` first")
	}
	id, err := session.resolveOrg(ctx, positional[0])
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return wrapClientError(err)
	}
	profile, ok := file.Profiles[session.profile]
	if !ok {
		return usageError("profile %q is not stored locally; run `openfox auth login`", session.profile)
	}
	profile.Org = id
	file.Profiles[session.profile] = profile
	if err := store.Save(file); err != nil {
		return wrapClientError(err)
	}
	fmt.Fprintf(c.stdout, "Profile %q now uses organization %s.\n", session.profile, id)
	return nil
}

func (c *cli) runMe(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("me", flag.ContinueOnError)
	cf.register(fs, false)
	positional, err := c.parse(fs, cf, "openfox me", args)
	if err != nil {
		return ignoreHelp(err)
	}
	if len(positional) != 0 {
		return usageError("usage: openfox me")
	}
	session, err := c.open(ctx, cf, false)
	if err != nil {
		return err
	}
	body, err := session.get(ctx, "/v1/me", nil)
	if err != nil {
		return err
	}
	return session.render(body, func(me map[string]any) {
		c.printPairs([][2]string{{"id", field(me, "id")}, {"email", field(me, "email")}, {"name", field(me, "full_name")}})
	})
}
