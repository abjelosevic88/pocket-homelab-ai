package server

import "testing"

func TestParseDeployNow(t *testing.T) {
	v := parseDeployNow("502101990ae8508bb46f0a5b35d2b4cbb3034692\ndevelopment\n")
	if v.Now != "502101990ae8508bb46f0a5b35d2b4cbb3034692" || v.Branch != "development" || !v.Checked {
		t.Fatalf("sha + branch: %+v", v)
	}
	v = parseDeployNow("abc1234\n{\n  \"branch\": \"feat/x\",\n  \"subject\": \"Fix | it\",\n  \"by\": \"Aleksandar\"\n}\n")
	if v.Now != "abc1234" || v.Branch != "feat/x" || v.Info["by"] != "Aleksandar" {
		t.Fatalf("sha + json: %+v", v)
	}
	if v = parseDeployNow("origin/feat/y\n"); v.Now != "origin/feat/y" || v.Branch != "feat/y" {
		t.Fatalf("branch only: %+v", v)
	}
}

func TestTrackCount(t *testing.T) {
	if trackCount("ahead 2, behind 13", "ahead") != 2 || trackCount("ahead 2, behind 13", "behind") != 13 || trackCount("gone", "ahead") != 0 {
		t.Fatal("trackCount")
	}
}
