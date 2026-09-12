package github

import (
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/src/App.jsx b/src/App.jsx
index 1..2 100644
--- a/src/App.jsx
+++ b/src/App.jsx
@@ -1 +1 @@
-old
+new
diff --git a/package-lock.json b/package-lock.json
index 3..4 100644
--- a/package-lock.json
+++ b/package-lock.json
@@ -1 +1 @@
-lock
+lock2
diff --git a/dist/bundle.js b/dist/bundle.js
index 5..6 100644
--- a/dist/bundle.js
+++ b/dist/bundle.js
@@ -1 +1 @@
-x
+y
diff --git a/README.md b/README.md
index 7..8 100644
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-a
+b
`

func TestTrimDiffDropsNoise(t *testing.T) {
	trimmed, notes := TrimDiff(sampleDiff)
	if !strings.Contains(trimmed, "src/App.jsx") || !strings.Contains(trimmed, "README.md") {
		t.Errorf("kept files missing:\n%s", trimmed)
	}
	if strings.Contains(trimmed, "package-lock.json") || strings.Contains(trimmed, "dist/bundle.js") {
		t.Errorf("noisy files kept:\n%s", trimmed)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2 file(s)") {
		t.Errorf("unexpected notes: %v", notes)
	}
	if got := ChangedFiles(sampleDiff); len(got) != 4 || got[0] != "src/App.jsx" || got[3] != "README.md" {
		t.Errorf("ChangedFiles = %v", got)
	}
}
