package review

import (
	"testing"
)

func TestSplitDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty diff",
			input:    "",
			expected: map[string]string{},
		},
		{
			name: "single file",
			input: `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
`,
			expected: map[string]string{
				"main.go": `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
`,
			},
		},
		{
			name: "multiple files with add modify delete",
			input: `diff --git a/existing.go b/existing.go
index 1111111..2222222 100644
--- a/existing.go
+++ b/existing.go
@@ -1,3 +1,3 @@
 package main
-var x = 1
+var x = 2
diff --git a/newfile.ts b/newfile.ts
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/newfile.ts
@@ -0,0 +1,2 @@
+const a = 1;
+export default a;
diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
index 4444444..0000000
--- a/deleted.txt
+++ /dev/null
@@ -1 +0,0 @@
-old content
`,
			expected: map[string]string{
				"existing.go": `diff --git a/existing.go b/existing.go
index 1111111..2222222 100644
--- a/existing.go
+++ b/existing.go
@@ -1,3 +1,3 @@
 package main
-var x = 1
+var x = 2
`,
				"newfile.ts": `diff --git a/newfile.ts b/newfile.ts
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/newfile.ts
@@ -0,0 +1,2 @@
+const a = 1;
+export default a;
`,
				"deleted.txt": `diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
index 4444444..0000000
--- a/deleted.txt
+++ /dev/null
@@ -1 +0,0 @@
-old content
`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := splitDiff(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d entries, got %d", len(tt.expected), len(result))
			}
			for key, expectedVal := range tt.expected {
				got, ok := result[key]
				if !ok {
					t.Errorf("missing key %q", key)
					continue
				}
				if got != expectedVal {
					t.Errorf("key %q:\nexpected:\n%s\ngot:\n%s", key, expectedVal, got)
				}
			}
		})
	}
}
