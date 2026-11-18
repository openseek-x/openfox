package main

// 协作 CLI：space / task / agent / search。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"openfox/internal/cliclient"
)

var taskPriorities = map[string]bool{"NONE": true, "LOWEST": true, "LOW": true, "NORMAL": true, "HIGH": true, "URGENT": true}

func (c *cli) runSpace(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox space <list|get>")
	}
	cf := &commonFlags{}
	fs := flag.NewFlagSet("space "+args[0], flag.ContinueOnError)
	cf.register(fs, true)
	switch args[0] {
	case "list":
		positional, err := c.parse(fs, cf, "openfox space list", args[1:])
		if err != nil {
			return ignoreHelp(err)
		}
		if len(positional) != 0 {
			return usageError("usage: openfox space list")
		}
		session, err := c.open(ctx, cf, true)
		if err != nil {
			return err
		}
		body, err := session.get(ctx, session.orgPath("spaces"), nil)
		if err != nil {
			return err
		}
		return session.render(body, func(value map[string]any) {
			var rows [][]string
			for _, space := range objectsAt(value, "spaces") {
				rows = append(rows, []string{field(space, "id"), field(space, "prefix"), field(space, "name"), field(space, "visibility")})
			}
			c.printTable([]string{"ID", "PREFIX", "NAME", "VISIBILITY"}, rows)
		})
	case "get":
		positional, err := c.parse(fs, cf, "openfox space get <prefix|id>", args[1:])
		if err != nil {
			return ignoreHelp(err)
		}
		if len(positional) != 1 {
			return usageError("usage: openfox space get <prefix|id>")
		}
		session, err := c.open(ctx, cf, true)
		if err != nil {
			return err
		}
		id, err := session.resolveSpace(ctx, positional[0])
		if err != nil {
			return err
		}
		body, err := session.get(ctx, session.orgPath("spaces/"+id), nil)
		if err != nil {
			return err
		}
		return session.render(body, func(space map[string]any) {
			c.printPairs([][2]string{{"id", field(space, "id")}, {"prefix", field(space, "prefix")}, {"name", field(space, "name")},
				{"visibility", field(space, "visibility")}})
		})
	}
	return usageError("unknown space command %q", args[0])
}

// taskInfo 是已加载的任务：定位信息 + 完整详情。
type taskInfo struct {
	space, id, version string
	body               []byte
	value              map[string]any
}

// loadTask 按显示编号（OPEN-12，含历史编号）或 UUID（需 --space）读取任务详情。
func (s *cliSession) loadTask(ctx context.Context, ref, spaceRef string) (*taskInfo, error) {
	var path string
	switch {
	case displayIDPattern.MatchString(ref):
		path = s.orgPath("tasks/by-display-id/" + url.PathEscape(ref))
	case uuidPattern.MatchString(ref):
		if spaceRef == "" {
			return nil, usageError("a task UUID needs --space; or use the display id, e.g. OPEN-12")
		}
		spaceID, err := s.resolveSpace(ctx, spaceRef)
		if err != nil {
			return nil, err
		}
		path = s.orgPath("spaces/" + spaceID + "/tasks/" + ref)
	default:
		return nil, usageError("%q is not a task reference; use a display id like OPEN-12 or a UUID with --space", ref)
	}
	body, err := s.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	value, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	return &taskInfo{space: field(value, "space_id"), id: field(value, "id"), version: field(value, "version"), body: body, value: value}, nil
}

func (s *cliSession) taskPath(t *taskInfo, rest string) string {
	path := s.orgPath("spaces/" + t.space + "/tasks/" + t.id)
	if rest != "" {
		path += "/" + rest
	}
	return path
}

func assigneeNames(task map[string]any) string {
	var names []string
	for _, a := range objectsAt(task, "assignees") {
		names = append(names, field(a, "name"))
	}
	return strings.Join(names, ", ")
}

func (c *cli) printTask(task map[string]any) {
	var labels []string
	for _, l := range objectsAt(task, "label_items") {
		labels = append(labels, field(l, "name"))
	}
	c.printPairs([][2]string{
		{"id", field(task, "id")}, {"display id", field(task, "display_id")}, {"title", field(task, "title")},
		{"type", field(task, "type_name")}, {"status", field(task, "status_name")}, {"priority", field(task, "priority")},
		{"assignees", assigneeNames(task)}, {"labels", strings.Join(labels, ", ")},
		{"start", field(task, "start_date")}, {"due", field(task, "due_date")}, {"version", field(task, "version")},
		{"description", field(task, "description")},
	})
}

func (c *cli) runTask(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox task <list|get|create|update|comment|run>")
	}
	switch args[0] {
	case "list":
		return ignoreHelp(c.taskList(ctx, args[1:]))
	case "get":
		return ignoreHelp(c.taskGet(ctx, args[1:]))
	case "create":
		return ignoreHelp(c.taskCreate(ctx, args[1:]))
	case "update":
		return ignoreHelp(c.taskUpdate(ctx, args[1:]))
	case "comment":
		return ignoreHelp(c.taskComment(ctx, args[1:]))
	case "run":
		return ignoreHelp(c.taskRun(ctx, args[1:]))
	}
	return usageError("unknown task command %q", args[0])
}

func (c *cli) taskList(ctx context.Context, args []string) error {
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	cf.register(fs, true)
	pf.register(fs)
	spaceRef := fs.String("space", "", "space prefix or id (required)")
	var statuses, assignees, labels, priorities stringList
	fs.Var(&statuses, "status", "status name or id; repeatable (any of)")
	fs.Var(&assignees, "assignee", "user:<id|me>, agent:<id|name>, crew:<id> or none; repeatable (any of)")
	fs.Var(&labels, "label", "label name or id; repeatable (any of)")
	fs.Var(&priorities, "priority", "NONE|LOWEST|LOW|NORMAL|HIGH|URGENT; repeatable (any of)")
	mine := fs.Bool("mine", false, "only tasks assigned to me (same as --assignee user:me)")
	q := fs.String("q", "", "keyword in title or description")
	usage := "openfox task list --space <prefix|id> [--status s] [--assignee a] [--mine] [--label l] [--priority p] [--q text] [--limit n] [--cursor c] [--all]"
	positional, err := c.parsePaged(fs, cf, pf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 || *spaceRef == "" {
		return usageError("usage: %s", usage)
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	spaceID, err := session.resolveSpace(ctx, *spaceRef)
	if err != nil {
		return err
	}
	query := url.Values{}
	for _, ref := range statuses {
		id, err := session.resolveStatus(ctx, ref)
		if err != nil {
			return err
		}
		query.Add("status_id", id)
	}
	if *mine {
		assignees = append(assignees, "user:me")
	}
	for _, ref := range assignees {
		if strings.EqualFold(ref, "none") {
			query.Add("assignee", "none")
			continue
		}
		kind, id, err := session.parseActor(ctx, spaceID, ref)
		if err != nil {
			return err
		}
		query.Add("assignee", kind+":"+id)
	}
	for _, ref := range labels {
		id, err := session.resolveLabel(ctx, spaceID, ref)
		if err != nil {
			return err
		}
		query.Add("label_id", id)
	}
	for _, p := range priorities {
		p = strings.ToUpper(p)
		if !taskPriorities[p] {
			return usageError("--priority %q is not valid", p)
		}
		query.Add("priority", p)
	}
	if *q != "" {
		query.Set("q", *q)
	}
	body, err := session.listPage(ctx, session.orgPath("spaces/"+spaceID+"/tasks"), query, "tasks", "cursor", pf)
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		var rows [][]string
		for _, t := range objectsAt(value, "tasks") {
			rows = append(rows, []string{field(t, "display_id"), field(t, "status_name"), field(t, "priority"), assigneeNames(t), field(t, "title")})
		}
		c.printTable([]string{"ID", "STATUS", "PRIORITY", "ASSIGNEES", "TITLE"}, rows)
		if next := field(value, "next_cursor"); next != "" {
			fmt.Fprintf(c.stderr, "more results: --cursor %s\n", next)
		}
	})
}

func (c *cli) taskGet(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("task get", flag.ContinueOnError)
	cf.register(fs, true)
	spaceRef := fs.String("space", "", "space prefix or id (only needed when the task is given as a UUID)")
	positional, err := c.parse(fs, cf, "openfox task get <OPEN-12> [--space s]", args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError("usage: openfox task get <OPEN-12>")
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	task, err := session.loadTask(ctx, positional[0], *spaceRef)
	if err != nil {
		return err
	}
	return session.render(task.body, c.printTask)
}

// taskFields 是 create/update 共用的任务字段 flag。
type taskFields struct {
	title, description, status, priority, start, due string
	assignees, labels                                stringList
}

func (tf *taskFields) register(fs *flag.FlagSet) {
	fs.StringVar(&tf.title, "title", "", "task title")
	fs.StringVar(&tf.description, "description", "", "task description")
	fs.StringVar(&tf.status, "status", "", "status name or id")
	fs.StringVar(&tf.priority, "priority", "", "NONE|LOWEST|LOW|NORMAL|HIGH|URGENT")
	fs.StringVar(&tf.start, "start", "", "start date YYYY-MM-DD")
	fs.StringVar(&tf.due, "due", "", "due date YYYY-MM-DD")
	fs.Var(&tf.assignees, "assignee", "user:<id|me>, agent:<id|name> or crew:<id>; repeatable")
	fs.Var(&tf.labels, "label", "label name or id; repeatable")
}

func checkDate(flagName, value string) error {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return usageError("--%s must be a date like 2026-12-31", flagName)
	}
	return nil
}

var actorKinds = map[string]string{"user": "HUMAN", "agent": "AGENT", "crew": "CREW"}

// apply 把已设置的字段写入请求体；clearable 为 true（update）时 none 表示清空。
func (tf *taskFields) apply(ctx context.Context, s *cliSession, spaceID string, set map[string]bool, body map[string]any, clearable bool) error {
	if set["title"] {
		if strings.TrimSpace(tf.title) == "" {
			return usageError("--title must not be empty")
		}
		body["title"] = tf.title
	}
	if set["description"] {
		body["description"] = tf.description
	}
	if set["status"] {
		id, err := s.resolveStatus(ctx, tf.status)
		if err != nil {
			return err
		}
		body["status_id"] = id
	}
	if set["priority"] {
		p := strings.ToUpper(tf.priority)
		if !taskPriorities[p] {
			return usageError("--priority %q is not valid", tf.priority)
		}
		body["priority"] = p
	}
	for name, value := range map[string]string{"start": tf.start, "due": tf.due} {
		if !set[name] {
			continue
		}
		key := name + "_date"
		if clearable && strings.EqualFold(value, "none") {
			body[key] = nil
			continue
		}
		if err := checkDate(name, value); err != nil {
			return err
		}
		body[key] = value
	}
	if set["assignee"] {
		refs := []map[string]string{}
		if !(clearable && len(tf.assignees) == 1 && strings.EqualFold(tf.assignees[0], "none")) {
			for _, ref := range tf.assignees {
				kind, id, err := s.parseActor(ctx, spaceID, ref)
				if err != nil {
					return err
				}
				refs = append(refs, map[string]string{"kind": actorKinds[kind], "id": id})
			}
		}
		body["assignees"] = refs
	}
	if set["label"] {
		ids := []string{}
		if !(clearable && len(tf.labels) == 1 && strings.EqualFold(tf.labels[0], "none")) {
			for _, ref := range tf.labels {
				id, err := s.resolveLabel(ctx, spaceID, ref)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
		}
		body["label_ids"] = ids
	}
	return nil
}

func (c *cli) taskCreate(ctx context.Context, args []string) error {
	cf, tf := &commonFlags{}, &taskFields{}
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	cf.register(fs, true)
	tf.register(fs)
	spaceRef := fs.String("space", "", "space prefix or id (required)")
	typeID := fs.String("type", "", "task type id (default: the space default type)")
	parent := fs.String("parent", "", "parent task display id or UUID")
	key := fs.String("idempotency-key", "", "Idempotency-Key (default: generated)")
	usage := "openfox task create --space <prefix|id> --title <text> [--description t] [--status s] [--priority p] [--assignee a] [--label l] [--start d] [--due d] [--parent OPEN-1] [--idempotency-key k]"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 || *spaceRef == "" || strings.TrimSpace(tf.title) == "" {
		return usageError("usage: %s", usage)
	}
	idemKey, err := c.idempotencyKey(*key)
	if err != nil {
		return err
	}
	if *typeID != "" && !uuidPattern.MatchString(*typeID) {
		return usageError("--type must be a task type id")
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	spaceID, err := session.resolveSpace(ctx, *spaceRef)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if err := tf.apply(ctx, session, spaceID, visited(fs), body, false); err != nil {
		return err
	}
	if *typeID != "" {
		body["type_id"] = *typeID
	}
	if *parent != "" {
		parentTask, err := session.loadTask(ctx, *parent, *spaceRef)
		if err != nil {
			return err
		}
		body["parent_task_id"] = parentTask.id
	}
	response, err := session.write(ctx, http.MethodPost, session.orgPath("spaces/"+spaceID+"/tasks"), body, idemKey, false)
	if err != nil {
		return err
	}
	return session.render(response, c.printTask)
}

func (c *cli) taskUpdate(ctx context.Context, args []string) error {
	cf, tf := &commonFlags{}, &taskFields{}
	fs := flag.NewFlagSet("task update", flag.ContinueOnError)
	cf.register(fs, true)
	tf.register(fs)
	spaceRef := fs.String("space", "", "space prefix or id (only needed when the task is given as a UUID)")
	expected := fs.Int("version", 0, "expected task version (default: the current version, read just before updating)")
	usage := "openfox task update <OPEN-12> [--title t] [--description t] [--status s] [--priority p] [--assignee a|none] [--label l|none] [--start d|none] [--due d|none] [--version n]"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return err
	}
	set := visited(fs)
	changed := false
	for _, name := range []string{"title", "description", "status", "priority", "start", "due", "assignee", "label"} {
		changed = changed || set[name]
	}
	if len(positional) != 1 || !changed {
		return usageError("usage: %s", usage)
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	task, err := session.loadTask(ctx, positional[0], *spaceRef)
	if err != nil {
		return err
	}
	body := map[string]any{"version": json.Number(task.version)}
	if set["version"] {
		body["version"] = *expected
	}
	if err := tf.apply(ctx, session, task.space, set, body, true); err != nil {
		return err
	}
	response, err := session.write(ctx, http.MethodPatch, session.taskPath(task, ""), body, "", false)
	if err != nil {
		return err
	}
	return session.render(response, c.printTask)
}

func (c *cli) taskComment(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox task comment <list|add> <OPEN-12>")
	}
	cf, pf := &commonFlags{}, &pageFlags{}
	fs := flag.NewFlagSet("task comment "+args[0], flag.ContinueOnError)
	cf.register(fs, true)
	spaceRef := fs.String("space", "", "space prefix or id (only needed when the task is given as a UUID)")
	switch args[0] {
	case "list":
		pf.register(fs)
		positional, err := c.parsePaged(fs, cf, pf, "openfox task comment list <OPEN-12> [--limit n] [--cursor c] [--all]", args[1:])
		if err != nil {
			return err
		}
		if len(positional) != 1 {
			return usageError("usage: openfox task comment list <OPEN-12>")
		}
		session, err := c.open(ctx, cf, true)
		if err != nil {
			return err
		}
		task, err := session.loadTask(ctx, positional[0], *spaceRef)
		if err != nil {
			return err
		}
		body, err := session.listPage(ctx, session.taskPath(task, "comments"), nil, "comments", "after", pf)
		if err != nil {
			return err
		}
		return session.render(body, func(value map[string]any) {
			var rows [][]string
			for _, comment := range objectsAt(value, "comments") {
				author := field(comment, "author_kind") + " " + firstNonEmpty(field(comment, "author_user_id"), field(comment, "author_agent_id"))
				rows = append(rows, []string{field(comment, "created_at"), author, field(comment, "body")})
			}
			c.printTable([]string{"CREATED_AT", "AUTHOR", "BODY"}, rows)
			if next := field(value, "next_cursor"); next != "" && next != "0" {
				fmt.Fprintf(c.stderr, "more results: --cursor %s\n", next)
			}
		})
	case "add":
		text := fs.String("body", "", "comment text (default: read from stdin)")
		replyTo := fs.String("reply-to", "", "reply to this comment id")
		key := fs.String("idempotency-key", "", "client_comment_id (default: generated)")
		positional, err := c.parsePaged(fs, cf, pf, "openfox task comment add <OPEN-12> [--body text] [--reply-to id] [--idempotency-key k]", args[1:])
		if err != nil {
			return err
		}
		if len(positional) != 1 {
			return usageError("usage: openfox task comment add <OPEN-12> [--body text]")
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
		task, err := session.loadTask(ctx, positional[0], *spaceRef)
		if err != nil {
			return err
		}
		request := map[string]any{"body": content, "client_comment_id": clientID}
		if *replyTo != "" {
			request["reply_to_id"] = *replyTo
		}
		response, err := session.write(ctx, http.MethodPost, session.taskPath(task, "comments"), request, "", true)
		if err != nil {
			return err
		}
		return session.render(response, func(comment map[string]any) {
			fmt.Fprintf(c.stdout, "Comment %s added to %s.\n", field(comment, "id"), field(task.value, "display_id"))
		})
	}
	return usageError("unknown comment command %q", args[0])
}

func (c *cli) taskRun(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("task run", flag.ContinueOnError)
	cf.register(fs, true)
	spaceRef := fs.String("space", "", "space prefix or id (only needed when the task is given as a UUID)")
	agentRef := fs.String("agent", "", "agent id or name; must already be an assignee of the task (required)")
	fresh := fs.Bool("new-conversation", false, "start a new assignment conversation instead of reusing the open one")
	key := fs.String("idempotency-key", "", "Idempotency-Key (default: generated)")
	usage := "openfox task run <OPEN-12> --agent <id|name> [--new-conversation] [--idempotency-key k]"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *agentRef == "" {
		return usageError("usage: %s", usage)
	}
	idemKey, err := c.idempotencyKey(*key)
	if err != nil {
		return err
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	task, err := session.loadTask(ctx, positional[0], *spaceRef)
	if err != nil {
		return err
	}
	agentID, err := session.resolveAgent(ctx, task.space, *agentRef)
	if err != nil {
		return err
	}
	body := map[string]any{"agent_id": agentID, "assignment_conversation": "reuse"}
	if *fresh {
		body["assignment_conversation"] = "new"
	}
	response, err := session.write(ctx, http.MethodPost, session.taskPath(task, "agent-runs"), body, idemKey, false)
	if err != nil {
		return err
	}
	return session.render(response, func(value map[string]any) {
		fmt.Fprintf(c.stdout, "Run queued for %s (agent %s, conversation: %s).\n", field(task.value, "display_id"), field(value, "agent_id"), field(value, "assignment_conversation"))
	})
}

func (c *cli) runAgent(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("usage: openfox agent <list|get>")
	}
	cf := &commonFlags{}
	fs := flag.NewFlagSet("agent "+args[0], flag.ContinueOnError)
	cf.register(fs, true)
	spaceRef := fs.String("space", "", "space prefix or id (required)")
	switch args[0] {
	case "list":
		archived := fs.Bool("include-archived", false, "include archived agents")
		positional, err := c.parse(fs, cf, "openfox agent list --space <prefix|id> [--include-archived]", args[1:])
		if err != nil {
			return ignoreHelp(err)
		}
		if len(positional) != 0 || *spaceRef == "" {
			return usageError("usage: openfox agent list --space <prefix|id>")
		}
		session, err := c.open(ctx, cf, true)
		if err != nil {
			return err
		}
		spaceID, err := session.resolveSpace(ctx, *spaceRef)
		if err != nil {
			return err
		}
		query := url.Values{}
		if *archived {
			query.Set("include_archived", "true")
		}
		body, err := session.get(ctx, session.orgPath("spaces/"+spaceID+"/agents"), query)
		if err != nil {
			return err
		}
		return session.render(body, func(value map[string]any) {
			var rows [][]string
			for _, a := range objectsAt(value, "agents") {
				rows = append(rows, []string{field(a, "id"), field(a, "name"), field(a, "kind"), field(a, "visibility"), field(a, "online")})
			}
			c.printTable([]string{"ID", "NAME", "KIND", "VISIBILITY", "ONLINE"}, rows)
		})
	case "get":
		positional, err := c.parse(fs, cf, "openfox agent get <id|name> --space <prefix|id>", args[1:])
		if err != nil {
			return ignoreHelp(err)
		}
		if len(positional) != 1 || *spaceRef == "" {
			return usageError("usage: openfox agent get <id|name> --space <prefix|id>")
		}
		session, err := c.open(ctx, cf, true)
		if err != nil {
			return err
		}
		spaceID, err := session.resolveSpace(ctx, *spaceRef)
		if err != nil {
			return err
		}
		agentID, err := session.resolveAgent(ctx, spaceID, positional[0])
		if err != nil {
			return err
		}
		body, err := session.get(ctx, session.orgPath("spaces/"+spaceID+"/agents/"+agentID), nil)
		if err != nil {
			return err
		}
		return session.render(body, func(a map[string]any) {
			c.printPairs([][2]string{{"id", field(a, "id")}, {"name", field(a, "name")}, {"kind", field(a, "kind")},
				{"visibility", field(a, "visibility")}, {"online", field(a, "online")}, {"description", field(a, "description")}})
		})
	}
	return usageError("unknown agent command %q", args[0])
}

func (c *cli) runSearch(ctx context.Context, args []string) error {
	cf := &commonFlags{}
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	cf.register(fs, true)
	types := fs.String("types", "", "comma-separated: task,project,view,agent,person,space,doc,conversation,message")
	limit := fs.Int("limit", 0, "results per type")
	offset := fs.Int("offset", 0, "skip this many results per type (needs --types)")
	spaceRef := fs.String("space", "", "restrict space-scoped types to this space")
	usage := "openfox search <text> [--types t1,t2] [--limit n] [--offset n] [--space s]"
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return ignoreHelp(err)
	}
	if len(positional) == 0 {
		return usageError("usage: %s", usage)
	}
	if *limit < 0 || *offset < 0 {
		return usageError("--limit and --offset must not be negative")
	}
	session, err := c.open(ctx, cf, true)
	if err != nil {
		return err
	}
	query := url.Values{"q": {strings.Join(positional, " ")}}
	if *types != "" {
		query.Set("types", *types)
	}
	if *limit > 0 {
		query.Set("limit", fmt.Sprint(*limit))
	}
	if visited(fs)["offset"] {
		query.Set("offset", fmt.Sprint(*offset))
	}
	if *spaceRef != "" {
		id, err := session.resolveSpace(ctx, *spaceRef)
		if err != nil {
			return err
		}
		query.Set("space_id", id)
	}
	body, err := session.call(ctx, cliclient.Request{Method: http.MethodGet, Path: session.orgPath("search"), Query: query})
	if err != nil {
		return err
	}
	return session.render(body, func(value map[string]any) {
		var rows [][]string
		for _, group := range objectsAt(value, "groups") {
			for _, item := range objectsAt(group, "items") {
				rows = append(rows, []string{field(group, "type"), field(item, "id"), field(item, "title"), field(item, "subtitle")})
			}
		}
		c.printTable([]string{"TYPE", "ID", "TITLE", "DETAIL"}, rows)
	})
}
