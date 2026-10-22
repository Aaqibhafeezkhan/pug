package testutil

import "testing"

// TestMain releases the containers this package's tests share. See Main.
func TestMain(m *testing.M) { Main(m) }
