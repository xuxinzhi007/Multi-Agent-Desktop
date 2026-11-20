package web

import (
	"testing"
)

func TestStepsOfKeepsSingleClick(t *testing.T) {
	ops, err := stepsOf([]byte(`{"op":"click","ref":"e24"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "click" || ops[0].Ref != "e24" {
		t.Fatalf("%#v", ops)
	}
}

func TestStepsOfRunsTheBatch(t *testing.T) {
	ops, err := stepsOf([]byte(`{"op":"click","steps":[{"op":"click","ref":"e19"},{"op":"click","ref":"e29"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Ref != "e19" || ops[1].Ref != "e29" {
		t.Fatalf("%#v", ops)
	}
}
