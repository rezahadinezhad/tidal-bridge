package main

import "testing"

func TestDevProcessPatterns(t *testing.T) {
	for command, want := range map[string]bool{
		`"C:\Program Files\nodejs\node.exe" R:\app\node_modules\next\dist\bin\next dev -p 3100`: true,
		`node node_modules/vite/bin/vite.js --port 5173`:                                        true,
		`node node_modules/vitest/vitest.mjs`:                                                   true,
		`node node_modules/vitest/vitest.mjs run`:                                               false,
		`python manage.py runserver 8001`:                                                       true,
		`python -m celery -A config worker -l info`:                                             true,
		`node node_modules/typescript/bin/tsc --noEmit`:                                         false,
		`node R:\Tidal Bridge\tests\fixtures\live-service\server.js`:                            false,
	} {
		if got := devProcess.MatchString(command); got != want {
			t.Errorf("%s: got %v, want %v", command, got, want)
		}
	}
}
