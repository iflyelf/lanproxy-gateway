# syntax=docker/dockerfile:1
#############################
#     设置公共的变量         #
#############################
ARG BASE_IMAGE_TAG=resolute
FROM ubuntu:${BASE_IMAGE_TAG}

# 作者描述信息
LABEL org.opencontainers.image.authors="iflyelf" \
      org.opencontainers.image.vendor="iflyelf" \
      org.opencontainers.image.source="https://github.com/iflyelf/lanproxy-gateway" \
      org.opencontainers.image.description="局域网透明代理网关(nftables TPROXY, 支持 IPv4/IPv6, 不依赖 eBPF/iptables)"

ARG TARGETARCH
ARG TARGETVARIANT

# 时区设置
ARG TZ=Asia/Shanghai
ENV TZ=$TZ
# 语言设置
ARG LANG=zh_CN.UTF-8
ENV LANG=$LANG

# 镜像变量
ARG DOCKER_IMAGE=iflyelf/lanproxy-gateway
ENV DOCKER_IMAGE=$DOCKER_IMAGE
ARG DOCKER_IMAGE_OS=ubuntu
ENV DOCKER_IMAGE_OS=$DOCKER_IMAGE_OS
ARG DOCKER_IMAGE_TAG=resolute
ENV DOCKER_IMAGE_TAG=$DOCKER_IMAGE_TAG

# 环境设置
ARG DEBIAN_FRONTEND=noninteractive
ENV DEBIAN_FRONTEND=$DEBIAN_FRONTEND

# GO环境变量
ARG GO_VERSION=1.26.4
ENV GO_VERSION=$GO_VERSION
ARG GOROOT=/opt/go
ENV GOROOT=$GOROOT
ARG GOPATH=/opt/golang
ENV GOPATH=$GOPATH
# Go 模块代理(加速依赖下载, 国内构建必备; 海外可改为 https://proxy.golang.org,direct)
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY

# 版本号(由 CI 通过 --build-arg VERSION=<git tag> 注入, 缺省为 dev)
ARG VERSION=dev
ENV VERSION=$VERSION

ARG PKG_DEPS="\
    zsh \
    bash \
    bash-doc \
    bash-completion \
    conntrack \
    ipset \
    ipvsadm \
    nftables \
    bind9-dnsutils \
    iproute2 \
    net-tools \
    iptables \
    bridge-utils \
    openvswitch-switch \
    libseccomp2 \
    nfs-common \
    rsync \
    socat \
    psmisc \
    procps \
    sysstat \
    firewalld \
    chrony \
    ntpsec-ntpdate \
    tcpdump \
    telnet \
    lsof \
    iftop \
    htop \
    nmap \
    nmap-common \
    jq \
    curl \
    wget \
    axel \
    git \
    vim \
    tree \
    unzip \
    zip \
    tar \
    subversion \
    lrzsz \
    gcc \
    g++ \
    build-essential \
    binutils \
    autoconf \
    automake \
    libtool \
    gettext \
    autopoint \
    asciidoc \
    gawk \
    patch \
    flex \
    texinfo \
    device-tree-compiler \
    zlib1g-dev \
    libjpeg-dev \
    libelf-dev \
    libssl-dev \
    openssl \
    libffi-dev \
    libglib2.0-dev \
    xmlto \
    libncurses-dev \
    locate \
    lvm2 \
    rsyslog \
    ca-certificates \
    gnupg2 \
    debsums \
    locales \
    tzdata \
    fonts-droid-fallback \
    fonts-wqy-zenhei \
    fonts-wqy-microhei \
    fonts-arphic-ukai \
    fonts-arphic-uming \
    language-pack-zh-hans \
    numactl \
    xz-utils \
    libaio-dev \
    python3 \
    python3-dev \
    python3-pip \
    python3-yaml \
    python3-venv \
    python-is-python3 \
    supervisor \
    tini \
    sshpass \
    iputils-ping \
    ncat \
    upx-ucl \
    libxml2-dev \
    libxslt1-dev \
    cargo \
    rustc \
    sudo \
    npm \
    uglifyjs"
ENV PKG_DEPS=$PKG_DEPS

# ***** 安装依赖 *****
RUN set -eux && \
   # 更新源地址
   sed -i 's@URIs: http://[a-z.]*\.ubuntu\.com/ubuntu/@URIs: https://mirrors.aliyun.com/ubuntu/@g' /etc/apt/sources.list.d/ubuntu.sources && \
   sed -i 's@^Types: deb$@Types: deb deb-src@' /etc/apt/sources.list.d/ubuntu.sources && \
   # 解决证书认证失败问题
   touch /etc/apt/apt.conf.d/99verify-peer.conf && echo >>/etc/apt/apt.conf.d/99verify-peer.conf "Acquire { https::Verify-Peer false }" && \
   # 更新系统软件
   DEBIAN_FRONTEND=noninteractive apt-get update -qqy && apt-get upgrade -qqy && \
   # 安装依赖包
   DEBIAN_FRONTEND=noninteractive apt-get install -qqy --no-install-recommends $PKG_DEPS --option=Dpkg::Options::=--force-confdef && \
   # multilib/i386 交叉编译包仅 amd64 架构提供, 其他架构跳过
   if [ "${TARGETARCH}" = "amd64" ]; then \
       DEBIAN_FRONTEND=noninteractive apt-get install -qqy --no-install-recommends \
           gcc-multilib g++-multilib libc6-dev-i386 --option=Dpkg::Options::=--force-confdef ; \
   fi && \
   DEBIAN_FRONTEND=noninteractive apt-get -qqy --no-install-recommends autoremove --purge && \
   DEBIAN_FRONTEND=noninteractive apt-get -qqy --no-install-recommends autoclean && \
   rm -rf /var/lib/apt/lists/* && \
   # 更新时区
   ln -sf /usr/share/zoneinfo/${TZ} /etc/localtime && \
   # 更新时间
   echo ${TZ} > /etc/timezone && \
   # 更改为zsh
   sh -c "$(curl -fsSL https://raw.github.com/ohmyzsh/ohmyzsh/master/tools/install.sh)" || true && \
   sed -i -e "s/bin\/ash/bin\/zsh/" /etc/passwd && \
   # vim 默认配置文件存在时才关闭 mouse(不同版本路径不同, 用 find 定位)
   find /usr/share/vim -name defaults.vim -exec sed -i -e 's/mouse=/mouse-=/g' {} + && \
   locale-gen zh_CN.UTF-8 && localedef -f UTF-8 -i zh_CN zh_CN.UTF-8 && locale-gen

# ***** 安装golang *****
RUN set -eux && \
    # 映射 buildx TARGETARCH 到 Go 官方包名 (arm -> armv6l, 其他直接用)
    case "${TARGETARCH}" in \
        amd64)   GO_ARCH=amd64   ;; \
        arm64)   GO_ARCH=arm64   ;; \
        arm)     GO_ARCH=armv6l  ;; \
        386)     GO_ARCH=386     ;; \
        *)       echo "不支持的架构: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    echo "目标架构: ${TARGETARCH} => Go 包: linux-${GO_ARCH}" && \
    wget --no-check-certificate https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz \
         -O /tmp/go-${GO_ARCH}.tar.gz && \
    tar xzf /tmp/go-${GO_ARCH}.tar.gz -C /opt && \
    mkdir -pv ${GOPATH}/bin && \
    rm -f /tmp/go-${GO_ARCH}.tar.gz && \
    # 软链 go 到 /usr/bin, 后续 RUN 层无需配 PATH
    ln -sf /opt/go/bin/* /usr/bin/ && \
    go version

# ***** 编译 lanproxy-gateway *****
# CGO_ENABLED=0 生成纯静态二进制; VERSION 注入生产版本号
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/opt/golang/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/opt/golang/pkg/mod \
    set -eux && \
    CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /usr/local/bin/lanproxy-gateway ./cmd/gateway && \
    /usr/local/bin/lanproxy-gateway --version

# ***** 运行配置 *****
WORKDIR /
EXPOSE 8088

# 容器需以 host 网络 + NET_ADMIN(或特权)运行, 详见 deploy/docker/docker-compose.yml
ENTRYPOINT ["/usr/local/bin/lanproxy-gateway"]
CMD ["run", "-c", "/etc/lanproxy-gateway/gateway.yaml"]
