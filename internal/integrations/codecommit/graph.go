package codecommit

import (
	"context"
	"errors"
	"sort"
)

type graph struct {
	ctx    context.Context
	client *Client
	creds  Credentials
	repo   string
	cache  map[string]Commit
}

func (g *graph) load(ids []string) error {
	missing := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := g.cache[id]; !ok && !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	if len(g.cache)+len(missing) > MaxGraphObjects {
		return ErrGraphBudget
	}
	for len(missing) > 0 {
		if err := g.ctx.Err(); err != nil {
			return err
		}
		n := len(missing)
		if n > 100 {
			n = 100
		}
		batch, err := g.client.BatchGetCommits(g.ctx, g.creds, g.repo, missing[:n])
		if err != nil {
			return err
		}
		for _, c := range batch {
			g.cache[c.ID] = c
		}
		missing = missing[n:]
	}
	return nil
}
func (g *graph) one(id string) (Commit, error) {
	if c, ok := g.cache[id]; ok {
		return c, nil
	}
	if len(g.cache) >= MaxGraphObjects {
		return Commit{}, ErrGraphBudget
	}
	c, err := g.client.GetCommit(g.ctx, g.creds, g.repo, id)
	if err != nil {
		return Commit{}, err
	}
	g.cache[id] = c
	return c, nil
}

// reachable closes every parent edge before returning; the stop set must have
// already been proven complete. No partial set escapes on a failed read.
func (g *graph) reachable(tip string, stop map[string]bool, limit int) (map[string]bool, error) {
	set := map[string]bool{}
	pending := []string{tip}
	for len(pending) > 0 {
		frontier := []string{}
		for _, id := range pending {
			if stop[id] || set[id] {
				continue
			}
			set[id] = true
			frontier = append(frontier, id)
			if limit > 0 && len(set) > limit {
				return nil, ErrCommitLimit
			}
		}
		if err := g.load(frontier); err != nil {
			return nil, err
		}
		pending = nil
		for _, id := range frontier {
			pending = append(pending, g.cache[id].Parents...)
		}
	}
	// A global seen set alone would hide cycles. Validate the closed subgraph
	// with gray/black colors, including edges between shared ancestors.
	color := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		if stop[id] {
			return nil
		}
		if color[id] == 1 {
			return ErrGraphInvalid
		}
		if color[id] == 2 {
			return nil
		}
		if !set[id] {
			return ErrGraphInvalid
		}
		color[id] = 1
		for _, p := range g.cache[id].Parents {
			if e := visit(p); e != nil {
				return e
			}
		}
		color[id] = 2
		return nil
	}
	if e := visit(tip); e != nil {
		return nil, e
	}
	return set, nil
}

// PushCommits computes Reach(new)-Reach(old), not a first-parent approximation.
// An empty old tip means a new branch and deliberately returns only its tip.
// Deleted branches are handled by callers; an empty new tip is invalid here.
// Result order is deterministic SHA order. Budgets include both graphs and the
// fast path; errors always return nil, never a partial set of new commits.
func (c *Client) PushCommits(ctx context.Context, creds Credentials, repo, oldTip, newTip string) (result []Commit, err error) {
	if !ValidRepositoryName(repo) || !ValidSHA(newTip) || (oldTip != "" && !ValidSHA(oldTip)) || creds.Validate() != nil {
		return nil, ErrInvalid
	}
	if oldTip == newTip {
		return []Commit{}, nil
	}
	ctx, cancel := WithReadBudget(ctx)
	defer cancel()
	defer func() {
		if err != nil {
			result = nil
			if b, ok := ctx.Value(budgetKey{}).(*readBudget); ok && b.used.Load() > MaxReadBytes && errors.Is(err, ErrIncomplete) {
				err = ErrGraphBudget
			}
		}
	}()
	g := &graph{ctx: ctx, client: c, creds: creds, repo: repo, cache: map[string]Commit{}}
	if oldTip == "" {
		tip, e := g.one(newTip)
		if e != nil {
			return nil, e
		}
		return []Commit{tip}, nil
	}
	// Only an entirely single-parent path that actually reaches old is a proof.
	// If it ends at another root or encounters a merge, close both full graphs.
	linear := []Commit{}
	visited := map[string]bool{}
	next := newTip
	for next != oldTip {
		if visited[next] {
			return nil, ErrGraphInvalid
		}
		visited[next] = true
		node, e := g.one(next)
		if e != nil {
			return nil, e
		}
		linear = append(linear, node)
		if len(node.Parents) != 1 {
			break
		}
		next = node.Parents[0]
	}
	if next == oldTip {
		if len(linear) > MaxPushCommits {
			return nil, ErrCommitLimit
		}
		sort.Slice(linear, func(i, j int) bool { return linear[i].ID < linear[j].ID })
		return linear, nil
	}
	old, e := g.reachable(oldTip, nil, 0)
	if e != nil {
		return nil, e
	}
	fresh, e := g.reachable(newTip, old, MaxPushCommits)
	if e != nil {
		return nil, e
	}
	result = make([]Commit, 0, len(fresh))
	for id := range fresh {
		result = append(result, g.cache[id])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
