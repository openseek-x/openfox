package main

// 协作 CLI：conversation / run。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (c *cli) runConversation(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox conversation <list|get|send|complete>")
	}
	switch args[0] {
	case "list":
		return ignoreHelp(c.conversationList(ctx, args[1:]))
	case "get":
		return ignoreHelp(c.conversationGet(ctx, args[1:]))
	case "send":
		return ignoreHelp(c.conversationSend(ctx, args[1:]))
	case "complete":
		return ignoreHelp(c.conversationComplete(ctx, args[1:]))
	}
	return usageError("unknown conversation command %q", args[0])
}

func requireUUID(what, value string) error {
	if !uuidPattern.MatchString(value) {
		return usageError("%s must be a UUID", what)
	}
	return nil
}

func (c *cli) conversationList(ctx context.Context, args []string) error {
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("conversation list", flag.ContinueOnError)
	cf.register(fs, true)
	pf.register(fs)
	agent := fs.String("agent", "", "only conversations of this agent id")
	task := fs.String("task", "", "only conversations bound to this task (display id or UUID)")
	q := fs.String("q", "", "title contains")
	archived := fs.String("archived", "", "exclude (default), include or only")
	usage := "openfox conversation list [--agent id] [--task OPEN-12] [--q text] [--archived exclude|include|only] [--limit n] [--cursor c] [--all]"
	positional, err := c.parsePaged(fs, cf, pf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError("usage: %s", usage)
	}
	query := url.Values{}
	if *agent != "" {
		if err := requireUUID("--agent", *agent); err != nil {
			return err
		}
		query.Set("agent_id", *agent)
	}
	if *q != "" {
		query.Set("q", *q)
	}
	if *archived != "" {
		if *archived != "exclude" && *archived != "include" && *archived != "only" {
			return usageError("--archived must be exclude, include or only")
		}
		query.Set("archived", *archived)
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	if *task != "" {
		info, err := session.loadTask(ctx, *task, "")
		if err != nil {
			return err
		}
		query.Set("task_id", info.id)
	}
	body, err := session.listPage(ctx, session.orgPath("conversations"), query, "conversations", "cursor", pf)
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		var rows [][]string
		for _, conv := range objectsAt(value, "conversations") {
			rows = append(rows, []string{field(conv, "id"), field(conv, "title"), field(conv, "status"), field(conv, "updated_at")})
		}
		c.printTable([]string{"ID", "TITLE", "STATUS", "UPDATED_AT"}, rows)
		if next := field(value, "next_cursor"); next != "" {
			fmt.Fprintf(c.stderr, "more results: --cursor %s\n", next)
		}
	})
}

// conversationGet 读取会话摘要；加 --messages 时改为输出消息列表（按 seq 升序，游标为 after）。
func (c *cli) conversationGet(ctx context.Context, args []string) error {
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("conversation get", flag.ContinueOnError)
	cf.register(fs, true)
	pf.register(fs)
	messages := fs.Bool("messages", false, "list the messages instead of the summary (--limit/--cursor/--all apply)")
	usage := "openfox conversation get <id> [--messages [--limit n] [--cursor c] [--all]]"
	positional, err := c.parsePaged(fs, cf, pf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: %s", usage)
	}
	if err := requireUUID("conversation id", positional[0]); err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	path := session.orgPath("conversations/" + positional[0])
	if *messages {
		body, err := session.listPage(ctx, path+"/messages", nil, "messages", "after", pf)
		if err != nil {
			return err
		}
		return session.render(body, func(value map[string]any) {
			var rows [][]string
			for _, m := range objectsAt(value, "messages") {
				rows = append(rows, []string{field(m, "seq"), field(m, "author_kind"), field(m, "created_at"), field(m, "body")})
			}
			c.printTable([]string{"SEQ", "AUTHOR", "CREATED_AT", "BODY"}, rows)
			if next := field(value, "next_cursor"); next != "" && next != "0" {
				fmt.Fprintf(c.stderr, "more results: --cursor %s\n", next)
			}
		})
	}
	body, err := session.get(ctx, path, nil)
	if err != nil {
		return err
	}
	return session.render(body, func(conv map[string]any) {
		c.printPairs([][2]string{{"id", field(conv, "id")}, {"title", field(conv, "title")}, {"status", field(conv, "status")},
			{"task", field(conv, "task.display_id")}, {"version", field(conv, "version")}, {"updated", field(conv, "updated_at")}})
	})
}

func (c *cli) conversationSend(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("conversation send", flag.ContinueOnError)
	cf.register(fs, true)
	text := fs.String("body", "", "message text (default: read from stdin)")
	replyTo := fs.String("reply-to", "", "reply to this message id")
	key := fs.String("idempotency-key", "", "client_message_id (default: generated)")
	var mentions stringList
	fs.Var(&mentions, "mention", "user:<id|me> or agent:<id>; repeatable; an agent mention triggers a run")
	usage := "openfox conversation send <id> [--body text] [--mention agent:<id>] [--reply-to id] [--idempotency-key k]"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: %s", usage)
	}
	if err := requireUUID("conversation id", positional[0]); err != nil {
		return err
	}
	content, err := c.readBody(*text, visited(fs)["body"])
	if err != nil {
		return err
	}
	clientID, err := c.idempotencyKey(*key)
	if err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	request := map[string]any{"body": content, "client_message_id": clientID}
	if *replyTo != "" {
		request["reply_to_id"] = *replyTo
	}
	if len(mentions) > 0 {
		list := []map[string]string{}
		for _, ref := range mentions {
			kind, id, ok := strings.Cut(ref, ":")
			kind = strings.ToLower(kind)
			if !ok || (kind != "user" && kind != "agent") {
				return usageError("--mention %q is not valid; use user:<id|me> or agent:<id>", ref)
			}
			if kind == "user" && strings.EqualFold(id, "me") {
				if id, err = session.meID(ctx); err != nil {
					return err
				}
			}
			if err := requireUUID("--mention id", id); err != nil {
				return err
			}
			list = append(list, map[string]string{kind + "_id": id})
		}
		request["mentions"] = list
	}
	response, err := session.write(ctx, http.MethodPost, session.orgPath("conversations/"+positional[0]+"/messages"), request, "", true)
	if err != nil {
		return err
	}
	return session.render(response, func(m map[string]any) {
		fmt.Fprintf(c.stdout, "Message %s sent (seq %s).\n", field(m, "id"), field(m, "seq"))
	})
}

// conversationComplete 先读当前 version 再提交（乐观锁：期间被改动会得到 409，退出码 7）。
func (c *cli) conversationComplete(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("conversation complete", flag.ContinueOnError)
	cf.register(fs, true)
	completeTask := fs.Bool("complete-task", false, "also move the bound task to the first Done status")
	positional, err := c.parse(fs, cf, "openfox conversation complete <id> [--complete-task]", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox conversation complete <id> [--complete-task]")
	}
	if err := requireUUID("conversation id", positional[0]); err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	path := session.orgPath("conversations/" + positional[0])
	current, err := session.get(ctx, path, nil)
	if err != nil {
		return err
	}
	summary, err := decodeObject(current)
	if err != nil {
		return err
	}
	request := map[string]any{"version": json.Number(field(summary, "version")), "complete_task": *completeTask}
	response, err := session.write(ctx, http.MethodPost, path+"/complete", request, "", false)
	if err != nil {
		return err
	}
	return session.render(response, func(conv map[string]any) {
		fmt.Fprintf(c.stdout, "Conversation %s completed.\n", field(conv, "id"))
	})
}

func (c *cli) runRunGroup(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox run <list|get|events|cancel>")
	}
	switch args[0] {
	case "list":
		return ignoreHelp(c.runList(ctx, args[1:]))
	case "get":
		return ignoreHelp(c.runGet(ctx, args[1:]))
	case "events":
		return ignoreHelp(c.runEvents(ctx, args[1:]))
	case "cancel":
		return ignoreHelp(c.runCancel(ctx, args[1:]))
	}
	return usageError("unknown run command %q", args[0])
}

func runRows(value map[string]any) [][]string {
	var rows [][]string
	for _, r := range objectsAt(value, "runs") {
		rows = append(rows, []string{field(r, "id"), field(r, "state"), field(r, "agent_id"), field(r, "created_at")})
	}
	return rows
}

// runList 列出会话的 Run（--conversation），或某个 Agent 的运行历史（--agent + --space）。
func (c *cli) runList(ctx context.Context, args []string) error {
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("run list", flag.ContinueOnError)
	cf.register(fs, true)
	pf.register(fs)
	conversation := fs.String("conversation", "", "list the runs of this conversation id")
	agent := fs.String("agent", "", "list the run history of this agent (id or name; needs --space)")
	spaceRef := fs.String("space", "", "space prefix or id (with --agent)")
	usage := "openfox run list (--conversation <id> | --agent <id|name> --space <prefix|id>) [--limit n] [--cursor c] [--all]"
	positional, err := c.parsePaged(fs, cf, pf, usage, args)
	if err != nil {
		return err
	}
	byConversation, byAgent := *conversation != "", *agent != ""
	if len(positional) != 0 || byConversation == byAgent || (byAgent && *spaceRef == "") {
		return usageError("usage: %s", usage)
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	var body []byte
	if byConversation {
		if err := requireUUID("--conversation", *conversation); err != nil {
			return err
		}
		body, err = session.listPage(ctx, session.orgPath("conversations/"+*conversation+"/runs"), nil, "runs", "after", pf)
	} else {
		var spaceID, agentID string
		if spaceID, err = session.resolveSpace(ctx, *spaceRef); err != nil {
			return err
		}
		if agentID, err = session.resolveAgent(ctx, spaceID, *agent); err != nil {
			return err
		}
		body, err = session.listPage(ctx, session.orgPath("spaces/"+spaceID+"/agents/"+agentID+"/runs"), nil, "runs", "cursor", pf)
	}
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		c.printTable([]string{"ID", "STATE", "AGENT", "CREATED_AT"}, runRows(value))
	})
}

func (c *cli) runGet(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("run get", flag.ContinueOnError)
	cf.register(fs, true)
	positional, err := c.parse(fs, cf, "openfox run get <id>", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox run get <id>")
	}
	if err := requireUUID("run id", positional[0]); err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	body, err := session.get(ctx, session.orgPath("runs/"+positional[0]), nil)
	if err != nil {
		return err
	}
	return session.render(body, func(r map[string]any) {
		c.printPairs([][2]string{{"id", field(r, "id")}, {"state", field(r, "state")}, {"agent", field(r, "agent_id")},
			{"conversation", field(r, "conversation_id")}, {"task", field(r, "task_id")}, {"reason", field(r, "reason")},
			{"created", field(r, "created_at")}, {"updated", field(r, "updated_at")}})
	})
}

func (c *cli) runEvents(ctx context.Context, args []string) error {
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("run events", flag.ContinueOnError)
	cf.register(fs, true)
	pf.register(fs)
	positional, err := c.parsePaged(fs, cf, pf, "openfox run events <id> [--limit n] [--cursor c] [--all]", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox run events <id>")
	}
	if err := requireUUID("run id", positional[0]); err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	body, err := session.listPage(ctx, session.orgPath("runs/"+positional[0]+"/events"), nil, "events", "after", pf)
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		var rows [][]string
		for _, e := range objectsAt(value, "events") {
			rows = append(rows, []string{field(e, "stream_seq"), field(e, "type"), field(e, "occurred_at"), field(e, "safe_payload")})
		}
		c.printTable([]string{"SEQ", "TYPE", "OCCURRED_AT", "PAYLOAD"}, rows)
		if next := field(value, "next_cursor"); next != "" && next != "0" {
			fmt.Fprintf(c.stderr, "more results: --cursor %s\n", next)
		}
	})
}

func (c *cli) runCancel(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("run cancel", flag.ContinueOnError)
	cf.register(fs, true)
	key := fs.String("idempotency-key", "", "Idempotency-Key (default: generated)")
	positional, err := c.parse(fs, cf, "openfox run cancel <id> [--idempotency-key k]", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox run cancel <id> [--idempotency-key k]")
	}
	if err := requireUUID("run id", positional[0]); err != nil {
		return err
	}
	idemKey, err := c.idempotencyKey(*key)
	if err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	body, err := session.write(ctx, http.MethodPost, session.orgPath("runs/"+positional[0]+"/cancel"), nil, idemKey, false)
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		fmt.Fprintf(c.stdout, "Run %s is now %s.\n", positional[0], field(value, "run.state"))
	})
}
