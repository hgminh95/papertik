CARGO ?= $(shell command -v cargo || echo $(HOME)/.cargo/bin/cargo)
DATA ?= data

.PHONY: all build vecdb server web synth index run-vecdb run-server dev test clean

all: build

build: vecdb server web

vecdb:
	cd vecdb && $(CARGO) build --release

server:
	cd server && go build -o bin/server .

web:
	cd web && npm install --no-audit --no-fund && npm run build

# Fake dataset for development (no Python/GPU needed).
synth: vecdb
	./vecdb/target/release/vecdb synth --n 100000 --out $(DATA)

# Real data: fetch + embed (see ingest/), then build the index.
fetch:
	uv run ingest/fetch.py --out $(DATA)/papers.jsonl --max 20000 api --sort cited_by_count:desc
embed:
	uv run ingest/embed.py --papers $(DATA)/papers.jsonl --out $(DATA)/embeddings.f32

index: vecdb
	./vecdb/target/release/vecdb build --papers $(DATA)/papers.jsonl --embeddings $(DATA)/embeddings.f32 --out $(DATA)/index.bin

run-vecdb:
	./vecdb/target/release/vecdb serve --index $(DATA)/index.bin

run-server:
	./server/bin/server -index $(DATA)/index.bin -papers $(DATA)/papers.jsonl -static web/dist

# Frontend with hot reload on :5173, proxied to the Go server on :8080.
dev:
	cd web && npm run dev

test:
	cd vecdb && $(CARGO) test --release
	cd server && go vet ./... && go test ./...
	cd web && npm run check

clean:
	rm -rf vecdb/target server/bin web/dist
