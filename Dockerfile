#############################
#  构建阶段: 编译 Go 二进制  #
#############################
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION} AS builder

# Go 模块代理(默认官方源,适配 GitHub runner;国内构建可传入 goproxy.cn)
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
ENV CGO_ENABLED=0

WORKDIR /src

# 先拷贝依赖清单以复用缓存层
COPY go.mod go.sum ./
RUN go mod download

# 拷贝源码并编译
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/lanproxy-gateway ./cmd/gateway

#############################
#  运行阶段: 精简运行镜像    #
#############################
FROM ubuntu:resolute

LABEL org.opencontainers.image.authors="iflyelf" \
      org.opencontainers.image.source="https://github.com/iflyelf/lanproxy-gateway" \
      org.opencontainers.image.description="局域网透明代理网关(nftables TPROXY, 不依赖 eBPF/iptables)"

ARG TZ=Asia/Shanghai
ENV TZ=$TZ

# 运行期依赖: nftables 提供 nft, iproute2 提供 ip 命令
# 使用默认 Ubuntu 源(GitHub runner 访问良好);国内构建可自行替换镜像源。
RUN set -eux && \
    apt-get update -qqy && \
    apt-get install -qqy --no-install-recommends \
        nftables \
        iproute2 \
        ca-certificates \
        tzdata && \
    ln -sf /usr/share/zoneinfo/${TZ} /etc/localtime && \
    echo ${TZ} > /etc/timezone && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/lanproxy-gateway /usr/local/bin/lanproxy-gateway

EXPOSE 8088

# 容器需以 host 网络 + 特权(或 CAP_NET_ADMIN)运行,详见 docker-compose.yml
ENTRYPOINT ["/usr/local/bin/lanproxy-gateway"]
CMD ["run", "-c", "/etc/lanproxy-gateway/gateway.yaml"]
