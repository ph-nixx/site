.PHONY: dev css server

dev:
	@trap 'kill 0' EXIT; \
	$(MAKE) css & \
	$(MAKE) server & \
	wait

css:
	tailwindcss -i ./static/input.css -o ./static/tailwind.css --watch

server:
	air
