package mono

import (
	"testing"
	"time"
)

func TestSub(t *testing.T) {
	t100 := Time(100)
	t200 := Time(200)

	if v := t100.Sub(t200); v != 0 {
		t.Errorf("Expected 0, got %v", v)
	}

	if v := t100.Sub(t100); v != 0 {
		t.Errorf("Expected 0, got %v", v)
	}

	if v := t200.Sub(t100); v != 100 {
		t.Errorf("Expected 100, got %v", v)
	}

	if !t100.Before(t200) {
		t.Errorf("100 is not before 200")
	}

	if t100.Before(t100) {
		t.Errorf("100 is before 100")
	}

	if t200.Before(t100) {
		t.Errorf("200 is before 100")
	}
}

func TestNow(t *testing.T) {
	t1 := Now()
	time.Sleep(3 * time.Second / 2)
	if v := Since(t1); v != 1 {
		t.Errorf("Expected 1, got %v", v)
	}
}

