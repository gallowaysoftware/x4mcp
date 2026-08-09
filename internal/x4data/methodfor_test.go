package x4data

import "testing"

// medicalSupplies mirrors the real ware: the same output, built from whatever
// each race can grow. Argon uses wheat, Paranid soja beans, Teladi flowers.
func medicalSupplies() Ware {
	return Ware{ID: "medicalsupplies", Name: "Medical Supplies", Methods: []Method{
		{Method: "default", Time: 300, Amount: 208, Inputs: []WareQty{
			{Ware: "energycells", Amount: 100}, {Ware: "spices", Amount: 40},
			{Ware: "water", Amount: 60}, {Ware: "wheat", Amount: 30}}},
		{Method: "paranid", Time: 300, Amount: 208, Inputs: []WareQty{
			{Ware: "energycells", Amount: 100}, {Ware: "sojabeans", Amount: 10},
			{Ware: "spices", Amount: 40}, {Ware: "water", Amount: 60}}},
		{Method: "teladi", Time: 300, Amount: 208, Inputs: []WareQty{
			{Ware: "energycells", Amount: 100}, {Ware: "spices", Amount: 40},
			{Ware: "sunriseflowers", Amount: 12}, {Ware: "water", Amount: 60}}},
	}}
}

func inputsOf(m Method) map[string]int {
	out := map[string]int{}
	for _, in := range m.Inputs {
		out[in.Ware] = in.Amount
	}
	return out
}

func TestMethodForPicksTheRaceVariant(t *testing.T) {
	w := medicalSupplies()
	cases := []struct {
		race    string
		wants   string // an input only this race uses
		forbids string // an input this race must NOT be charged
	}{
		{race: "teladi", wants: "sunriseflowers", forbids: "wheat"},
		{race: "paranid", wants: "sojabeans", forbids: "wheat"},
		{race: "argon", wants: "wheat", forbids: "sunriseflowers"},
		// No variant for this race, and no race at all: fall back to default.
		{race: "boron", wants: "wheat", forbids: "sunriseflowers"},
		{race: "", wants: "wheat", forbids: "sunriseflowers"},
		{race: "  TELADI  ", wants: "sunriseflowers", forbids: "wheat"},
	}
	for _, tc := range cases {
		t.Run(tc.race, func(t *testing.T) {
			m, ok := w.MethodFor(tc.race)
			if !ok {
				t.Fatalf("MethodFor(%q) returned no method", tc.race)
			}
			in := inputsOf(m)
			if _, has := in[tc.wants]; !has {
				t.Errorf("MethodFor(%q) inputs %v, want it to include %q", tc.race, in, tc.wants)
			}
			if _, has := in[tc.forbids]; has {
				t.Errorf("MethodFor(%q) charges %q, which that race's module never buys", tc.race, tc.forbids)
			}
		})
	}
}

// A ware with a single recipe behaves the same for everyone — Nostrop Oil is
// Teladi-flavoured but has only one method, so race must not change it.
func TestMethodForSingleRecipeWare(t *testing.T) {
	w := Ware{ID: "nostropoil", Methods: []Method{
		{Method: "default", Time: 300, Amount: 500, Inputs: []WareQty{
			{Ware: "energycells", Amount: 100}, {Ware: "spices", Amount: 40},
			{Ware: "sunriseflowers", Amount: 40}, {Ware: "water", Amount: 60}}},
	}}
	for _, race := range []string{"teladi", "argon", ""} {
		m, ok := w.MethodFor(race)
		if !ok {
			t.Fatalf("MethodFor(%q): no method", race)
		}
		if _, has := inputsOf(m)["sunriseflowers"]; !has {
			t.Errorf("MethodFor(%q) lost the flowers", race)
		}
		if _, has := inputsOf(m)["wheat"]; has {
			t.Errorf("MethodFor(%q) invented wheat; nostrop oil has one recipe and it uses flowers", race)
		}
	}
}

func TestMethodForRawWare(t *testing.T) {
	if _, ok := (Ware{ID: "ore"}).MethodFor("teladi"); ok {
		t.Error("a mined ware has no production method")
	}
}
