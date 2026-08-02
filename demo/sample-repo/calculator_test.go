package sample

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}

func TestDivideByZero(t *testing.T) {
	if _, err := Divide(10, 0); err != ErrDivisionByZero {
		t.Fatalf("Divide(10, 0) error = %v, want %v", err, ErrDivisionByZero)
	}
}
