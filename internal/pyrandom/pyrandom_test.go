package pyrandom

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"testing"
)

// The vectors are CPython's (testdata/vectors.py, run once): every call in the same order, so a
// state that drifts shows in all that follows.
type vector struct {
	Seed          json.Number
	Random        []float64
	Bits          [][2]json.Number
	Randbelow     [][2]json.Number
	Randrange     [][3]int
	RandrangeStep [][4]int `json:"randrange_step"`
	Randint       [][3]int
	Choice        [][2]int
	Sample        [][3]json.RawMessage
	Shuffle       []int
	Choices       []int
	ChoicesW      []int `json:"choices_w"`
	Uniform       []float64
}

func TestCPythonVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&vs); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		seed, _ := new(big.Int).SetString(v.Seed.String(), 10)
		r := New(seed.Int64())
		for i, want := range v.Random {
			if got := r.Random(); got != want {
				t.Fatalf("seed %s random %d: %v, want %v", v.Seed, i, got, want)
			}
		}
		for _, b := range v.Bits {
			k, _ := b[0].Int64()
			want, _ := new(big.Int).SetString(b[1].String(), 10)
			if got := r.Getrandbits(int(k)); got != want.Uint64() {
				t.Fatalf("seed %s getrandbits(%d): %d, want %s", v.Seed, k, got, want)
			}
		}
		for _, b := range v.Randbelow {
			n, _ := b[0].Int64()
			want, _ := b[1].Int64()
			if got := r.Randbelow(int(n)); int64(got) != want {
				t.Fatalf("seed %s randbelow(%d): %d, want %d", v.Seed, n, got, want)
			}
		}
		for _, x := range v.Randrange {
			if got := r.Randrange(x[0], x[1]); got != x[2] {
				t.Fatalf("seed %s randrange%v: %d", v.Seed, x, got)
			}
		}
		for _, x := range v.RandrangeStep {
			if got := r.RandrangeStep(x[0], x[1], x[2]); got != x[3] {
				t.Fatalf("seed %s randrange%v: %d", v.Seed, x, got)
			}
		}
		for _, x := range v.Randint {
			if got := r.Randint(x[0], x[1]); got != x[2] {
				t.Fatalf("seed %s randint%v: %d", v.Seed, x, got)
			}
		}
		for _, x := range v.Choice {
			if got := r.Choice(x[0]); got != x[1] {
				t.Fatalf("seed %s choice%v: %d", v.Seed, x, got)
			}
		}
		for _, x := range v.Sample {
			var n, k int
			var want []int
			json.Unmarshal(x[0], &n)
			json.Unmarshal(x[1], &k)
			json.Unmarshal(x[2], &want)
			if got := r.SampleIndexes(n, k); !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %s sample(%d, %d): %v, want %v", v.Seed, n, k, got, want)
			}
		}
		x := make([]int, 20)
		for i := range x {
			x[i] = i
		}
		Shuffle(r, x)
		if !reflect.DeepEqual(x, v.Shuffle) {
			t.Fatalf("seed %s shuffle: %v, want %v", v.Seed, x, v.Shuffle)
		}
		if got := r.ChoicesIndexes(10, nil, 6); !reflect.DeepEqual(got, v.Choices) {
			t.Fatalf("seed %s choices: %v, want %v", v.Seed, got, v.Choices)
		}
		if got := r.ChoicesIndexes(5, []float64{1, 0, 2.5, 3, 8}, 8); !reflect.DeepEqual(got, v.ChoicesW) {
			t.Fatalf("seed %s weighted choices: %v, want %v", v.Seed, got, v.ChoicesW)
		}
		for i, want := range v.Uniform {
			if got := r.Uniform(-2.5, 7.25); got != want {
				t.Fatalf("seed %s uniform %d: %v, want %v", v.Seed, i, got, want)
			}
		}
	}
}
