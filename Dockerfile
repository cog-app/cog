ARG DEPS_BASE
ARG BUILD_BASE

FROM ${DEPS_BASE:-debian:trixie-slim} AS dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
	tesseract-ocr \
	tesseract-ocr-eng \
	ffmpeg \
	ca-certificates \
	libstdc++6 \
	wget \
	yt-dlp \
	&& rm -rf /var/lib/apt/lists/*
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
