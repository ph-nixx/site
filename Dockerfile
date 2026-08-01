FROM rustlang/rust:nightly-bookworm as builder

# Pin the nightly. RUSTUP_TOOLCHAIN outranks rust-toolchain.toml, so the app build
# below cannot drift onto a newer nightly.
ENV RUSTUP_TOOLCHAIN=nightly-2026-07-20
RUN rustup toolchain install $RUSTUP_TOOLCHAIN

# Fetch the prebuilt cargo-leptos. The releases/download endpoint needs no GitHub API
# call, so it avoids the rate limit that otherwise forces a source build.
ARG CARGO_LEPTOS_VERSION=0.3.7
ARG TARGET=x86_64-unknown-linux-gnu
RUN wget -qO- https://github.com/leptos-rs/cargo-leptos/releases/download/v${CARGO_LEPTOS_VERSION}/cargo-leptos-${TARGET}.tar.gz \
    | tar -xz -C /usr/local/cargo/bin --strip-components=1 cargo-leptos-${TARGET}/cargo-leptos
RUN mkdir -p /app
WORKDIR /app
COPY . .
RUN rustup target add wasm32-unknown-unknown
RUN cargo leptos build --release -vv

FROM debian:bookworm-slim as runtime
COPY --from=builder /app/target/release/site /app/bin
COPY --from=builder /app/target/site /app/site
WORKDIR /app

ENV RUST_LOG="info"
ENV LEPTOS_SITE_ROOT="site"
ENV LEPTOS_SITE_PKG_DIR="pkg"
ENV LEPTOS_SITE_ADDR=0.0.0.0:8080

EXPOSE 8080
ENTRYPOINT ["/app/bin"]
