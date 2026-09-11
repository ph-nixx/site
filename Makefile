.PHONY: dev css server

dev:
	@trap 'kill 0' EXIT; \
	$(MAKE) css & \
	$(MAKE) server & \
	wait

css:
	./tailwind -i ./views/style/input.css -o ./static/output.css --watch=always

server:
	air
