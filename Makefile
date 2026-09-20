BIN := plugin/bin/typesafe-gate
CORPUS := cmd/gate/corpus.json
HELDOUT := cmd/gate/corpus-heldout.json

SEMLINT := bin/semlint

.PHONY: build eval eval-heldout check install-omp install-claude clean semlint semlint-sites semlint-fixtures semlint-calibrate semlint-diff semlint-separation

build: $(BIN)

$(BIN): $(wildcard cmd/gate/*.go) $(wildcard typesafe/*.go)
	go build -o $(BIN) ./cmd/gate

# Thresholds were tuned against this corpus, so treat its score as fitted.
eval: build
	$(BIN) -mode=eval -corpus=$(CORPUS) $(ARGS)

# Written after tuning and run once. This is the number that means something.
eval-heldout: build
	$(BIN) -mode=eval -corpus=$(HELDOUT) $(ARGS)

install-claude: build
	@echo "claude --plugin-dir $(CURDIR)/plugin"

install-omp: build
	@echo "omp --hook $(CURDIR)/plugin/omp/typesafe-gate.hook.ts"
	@echo
	@echo "or add to ~/.omp/agent/config.yml:"
	@echo "hooks:"
	@echo "  - $(CURDIR)/plugin/omp/typesafe-gate.hook.ts"

# The semantic linter: a battery of rules pairing a deterministic site pattern
# with one judgment a regex cannot make.
semlint: $(SEMLINT)

$(SEMLINT): $(wildcard cmd/semlint/*.go) $(wildcard typesafe/*.go)
	@mkdir -p bin
	go build -o $(SEMLINT) ./cmd/semlint

# The deterministic pass alone. Costs nothing and shows what would be asked.
semlint-sites: $(SEMLINT)
	$(SEMLINT) -sites-only $(or $(PATHS),cmd/semlint/fixtures)

# Recall against planted defects, precision against their correct counterparts.
# Expectations live in fixtures/defects.expected.json, never in the source.
semlint-fixtures: $(SEMLINT)
	@echo "== browser-storage: planted defects =="
	@-$(SEMLINT) -sets=all cmd/semlint/fixtures/defects.js
	@echo
	@echo "== browser-storage: correct counterpart (findings here are suspect) =="
	@-$(SEMLINT) -sets=all cmd/semlint/fixtures/clean.js
	@echo
	@echo "== general rules: planted defects in Go =="
	@-$(SEMLINT) -sets=default cmd/semlint/fixtures/general.go
	@echo
	@echo "== cross-file: each file correct alone, the pair is not =="
	@-$(SEMLINT) -sets=browser-storage cmd/semlint/fixtures/crossfile

# Lint only what changed. This is the shape to use in CI: a package with
# thirteen thousand units has a handful after a normal commit.
semlint-diff: $(SEMLINT)
	$(SEMLINT) -sets=all -since=$(or $(SINCE),HEAD) $(ARGS)

# Grade every rule by how far apart its defective and correct populations sit.
# This is the measurement that decides whether a rule is worth shipping.
semlint-separation: $(SEMLINT)
	$(SEMLINT) -separation -sets=all -runs=$(or $(RUNS),3) \
	  cmd/semlint/fixtures/defects.js cmd/semlint/fixtures/clean.js cmd/semlint/fixtures/general.go

# Raw probabilities with every threshold dropped, for calibration. Always with
# the whole set active: an answer shifts when other questions share the request.
semlint-calibrate: $(SEMLINT)
	$(SEMLINT) -calibrate -sets=all $(or $(PATHS),cmd/semlint/fixtures/defects.js)

clean:
	rm -f $(BIN) $(SEMLINT)

.PHONY: sos sos-test sos-studio sos-desktop sos-test-live vscode-check vscode-test vscode-package
sos:
	go generate ./internal/sosbuild
	go build -trimpath -o bin/sos ./cmd/sos

sos-test:
	go test ./...
	go vet ./...

sos-test-live:
	SOS_LIVE_TEST=1 go test ./tests/e2e -run TestLiveJevAndReplay -v

sos-studio:
	pnpm --dir studio install --frozen-lockfile
	pnpm --dir studio build
	go build -trimpath -o bin/sos-studio ./cmd/sos-studio

sos-desktop: sos-studio
	cd desktop && go tool wails build -clean

vscode-check:
	pnpm --dir vscode check

vscode-test:
	pnpm --dir vscode test

vscode-package:
	pnpm --dir vscode package
