.PHONY: up down check
up:
	docker compose up --build -d --wait
down:
	docker compose down
check:
	sh scripts/check.sh
