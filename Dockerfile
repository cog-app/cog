ARG DEPS_BASE
ARG BUILD_BASE

# The image the test and pages jobs run in (docker build --target ci): the Go
# toolchain, a C compiler for go-sqlite3, Chromium for the browser tests, git
# for go tool stamp, and every module go.sum names, so a job neither installs
# packages nor downloads modules. The ci-image job rebuilds it when this file,
# go.mod or go.sum changes (.gitlab-ci.yml).
#
# It is the first stage because the legacy builder builds every stage above
# the target, whether the target needs it or not.
FROM golang:1.26-alpine AS ci
RUN apk add --no-cache gcc musl-dev chromium git
WORKDIR /tmp/modules
COPY go.mod go.sum ./
RUN go mod download && rm -rf /tmp/modules
WORKDIR /
ENV CGO_ENABLED=1

FROM ${DEPS_BASE:-debian:trixie-slim} AS dependencies
# bubblewrap sandboxes the programs that parse uploads (internal/sandbox).
# It needs to create user namespaces; where the container may not, they run
# unsandboxed, as the log and Admin > Plugins say.
RUN apt-get update && apt-get install -y --no-install-recommends \
	bubblewrap \
	tesseract-ocr \
	tesseract-ocr-eng \
	ffmpeg \
	ca-certificates \
	libstdc++6 \
	wget \
	unzip \
	&& rm -rf /var/lib/apt/lists/*
# yt-dlp from its own releases, not Debian's: YouTube changes often enough
# that a distribution's copy stops resolving streams long before the
# distribution updates it. deno is the JavaScript runtime yt-dlp runs
# YouTube's player code in; without one it finds fewer formats, or none.
# Bump both when YouTube breaks.
ARG YTDLP_VERSION=2026.10.26
ARG DENO_VERSION=v2.9.8
RUN set -eux; \
	case "$(uname -m)" in \
		x86_64) YTDLP_ASSET=yt-dlp_linux; DENO_ARCH=x86_64 ;; \
		aarch64) YTDLP_ASSET=yt-dlp_linux_aarch64; DENO_ARCH=aarch64 ;; \
		*) echo "unsupported arch"; exit 1 ;; \
	esac; \
	wget -q "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/${YTDLP_ASSET}" -O /usr/local/bin/yt-dlp && \
	chmod 755 /usr/local/bin/yt-dlp && \
	wget -q "https://github.com/denoland/deno/releases/download/${DENO_VERSION}/deno-${DENO_ARCH}-unknown-linux-gnu.zip" -O /tmp/deno.zip && \
	unzip -q /tmp/deno.zip -d /usr/local/bin && \
	rm /tmp/deno.zip && \
	yt-dlp --version && deno --version
# ONNX Runtime for image recognition
ARG ONNXRUNTIME_VERSION=1.24.3
RUN set -eux; \
	case "$(uname -m)" in \
		x86_64) ORT_ARCH=x64 ;; \
		aarch64) ORT_ARCH=aarch64 ;; \
		*) echo "unsupported arch"; exit 1 ;; \
	esac; \
	wget -q "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}.tgz" -O /tmp/ort.tgz && \
	tar -xzf /tmp/ort.tgz -C /tmp && \
	cp -a /tmp/onnxruntime-linux-${ORT_ARCH}-${ONNXRUNTIME_VERSION}/lib/libonnxruntime.so* /usr/lib/ && \
	ldconfig && \
	rm -rf /tmp/ort.tgz /tmp/onnxruntime-*
# LibreOffice is on its own line to minimize the size of the layer
RUN apt-get update && apt-get install -y --no-install-recommends \
	libreoffice-writer \
	libreoffice-calc \
	libreoffice-impress \
	fonts-noto-core \
	&& rm -rf /var/lib/apt/lists/*

FROM ${BUILD_BASE:-golang:1.26-trixie} AS build_dependencies
RUN apt-get update && apt-get install -y --no-install-recommends git gcc g++ cmake libc6-dev && rm -rf /var/lib/apt/lists/*
RUN git config --global --add safe.directory '*'

WORKDIR /code
COPY go.mod go.sum .
RUN go mod download # cache mod downloads

# whisper.cpp for the feed plugin's transcription backend.
FROM build_dependencies AS whisper_build
ARG WHISPER_CPP_VERSION=v1.7.4
RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} https://github.com/ggerganov/whisper.cpp /whisper.cpp && \
	cd /whisper.cpp && \
	cmake -B build -DBUILD_SHARED_LIBS=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON && \
	cmake --build build --config Release --target whisper-cli -j

FROM build_dependencies AS build
COPY . .
RUN CGO_ENABLED=1 go build ./cmd/cog
RUN CGO_ENABLED=1 go build -o cog-plugin-recipes ./cmd/cog-plugin-recipes

FROM dependencies AS publish
COPY --from=build /code/cog /usr/bin/cog
COPY --from=build /code/cog-plugin-recipes /usr/bin/cog-plugin-recipes
COPY --from=whisper_build /whisper.cpp/build/bin/whisper-cli /usr/local/bin/whisper-cli
RUN mkdir -p /etc/bash_completion.d && cog completion bash > /etc/bash_completion.d/cog 2>/dev/null || true
RUN cog doctor
EXPOSE 8580
ENV COG_ADDR=:8580 COG_DB=/data/system.db COG_DATA_DIR=/data
CMD [ "/usr/bin/cog" ]
