GOCACHE ?= /tmp/print-cam-gocache
IMAGE ?= print-cam:local

.PHONY: verify test test-integration frontend-install frontend-build browser-install browser-smoke docker-build

verify: test frontend-install frontend-build browser-install browser-smoke docker-build

test:
	GOCACHE=$(GOCACHE) go test ./...

test-integration:
	test -n "$$TEST_DATABASE_URL"
	GOCACHE=$(GOCACHE) go test ./...

frontend-install:
	cd frontend && npm ci

frontend-build:
	cd frontend && npm run build

browser-install:
	cd frontend && npx playwright install chromium

browser-smoke:
	cd frontend && npm run smoke

docker-build:
	docker build -t $(IMAGE) .
