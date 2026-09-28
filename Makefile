CARGO ?= $(shell command -v cargo || echo $(HOME)/.cargo/bin/cargo)
DATA ?= data

.PHONY: all build vecdb server web synth index ingest run-vecdb run-server dev test clean

all: build

build: vecdb server web

vecdb:
	cd vecdb && $(CARGO) build --release

server:
	cd server && go build -tags sqlite_fts5 -o bin/server .

web:
	cd web && npm install --no-audit --no-fund && npm run build

# Fake dataset for development (no Python/GPU needed).
synth: vecdb
	./vecdb/target/release/vecdb synth --n 100000 --out $(DATA)

# Real data: the ingest service fetches, embeds and (re)builds the index, forever.
# Leave it running; vecdb and the server pick up each new index by themselves.
ingest: vecdb
	uv run ingest/daemon.py --data $(DATA) --vecdb ./vecdb/target/release/vecdb

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
	cd server && go vet -tags sqlite_fts5 ./... && go test -tags sqlite_fts5 ./...
	cd web && npm run check

clean:
	rm -rf vecdb/target server/bin web/dist
