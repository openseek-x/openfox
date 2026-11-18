package spacebundle

import (
	"bytes"
	"testing"
)

func TestReadPrioritiesFromFinalBundle(t *testing.T) {
	data := build(t, func(w *Writer) {
		for _, v := range []struct {
			kind  string
			value any
		}{{KindTask, Task{ID: "lowest", Priority: "LOWEST"}}, {KindProject, Project{Name: "Lowest project", Priority: "LOWEST"}}, {KindProject, Project{Name: "Other project", Priority: "HIGH"}}, {KindProject, Project{Name: "Other project", Priority: "LOWEST"}}} {
			if err := w.Write(v.kind, v.value); err != nil {
				t.Fatal(err)
			}
		}
	})
	p, err := ReadPriorities(bytes.NewReader(data))
	if err != nil || p.Tasks != 1 || len(p.Projects) != 1 || p.Projects[0] != "Lowest project" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := ReadPriorities(bytes.NewReader([]byte("bad gzip"))); err == nil {
		t.Fatal("invalid artifact accepted")
	}
}
