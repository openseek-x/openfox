package spacebundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PriorityInventory includes only data that can introduce LOWEST in the destination.
// Projects reused by name are removed by the destination's single batch query.
type PriorityInventory struct {
	Tasks    int
	Projects []string
}

// ReadPriorities checks the final artifact rather than a source DB count taken before export.
// Reader enforces the decompressed size/line limits; task records are counted, never retained.
func ReadPriorities(src io.Reader) (PriorityInventory, error) {
	var p PriorityInventory
	r, err := NewReader(src)
	if err != nil {
		return p, err
	}
	defer r.Close()
	if _, err := r.ReadHeader(); err != nil {
		return p, err
	}
	seenProjects := map[string]bool{}
	for {
		kind, data, err := r.Next()
		if errors.Is(err, io.EOF) {
			return p, ErrTruncated
		}
		if err != nil {
			return p, err
		}
		switch kind {
		case KindEnd:
			return p, nil
		case KindTask, KindProject:
			var value struct {
				Priority string `json:"priority"`
				Name     string `json:"name"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return p, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			// cloneProject reuses the first project with the same name, including one
			// created earlier from this bundle. Later duplicate names never write priority.
			if kind == KindProject {
				key := strings.ToLower(value.Name)
				if seenProjects[key] {
					continue
				}
				seenProjects[key] = true
				if len(seenProjects) > MaxTasks {
					return p, ErrTooLarge
				}
			}
			if value.Priority != "LOWEST" {
				continue
			}
			if kind == KindTask {
				p.Tasks++
			} else {
				p.Projects = append(p.Projects, value.Name)
			}
			if p.Tasks > MaxTasks || len(p.Projects) > MaxTasks {
				return p, ErrTooLarge
			}
		}
	}
}
