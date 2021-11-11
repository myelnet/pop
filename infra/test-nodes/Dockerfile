FROM debian:latest

RUN apt-get update -y && \
    apt-get install -y build-essential curl wget git

ENV CGO_CFLAGS="-D__BLST_PORTABLE__"

ARG GO_VERSION=1.16.3
ARG GOLANG_DIST_SHA=951a3c7c6ce4e56ad883f97d9db74d3d6d80d5fec77455c6ada6c1f7ac4776d2

# update golang
RUN \
	GOLANG_DIST=https://storage.googleapis.com/golang/go${GO_VERSION}.linux-amd64.tar.gz && \
	wget -O go.tgz "$GOLANG_DIST" && \
	echo "${GOLANG_DIST_SHA} *go.tgz" | sha256sum -c - && \
	rm -rf /usr/local/go && \
	tar -C /usr/local -xzf go.tgz && \
	rm go.tgz

ENV PATH="/usr/local/go/bin:${PATH}"

RUN go version;

RUN wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
    apt-get install -y ./google-chrome-stable_current_amd64.deb

RUN curl -s https://packagecloud.io/install/repositories/github/git-lfs/script.deb.sh | bash && \
    apt-get install git-lfs

RUN apt-get install -y procps

ADD "https://api.github.com/repos/myelnet/pop/commits?per_page=1" latest_commit
RUN git clone https://github.com/myelnet/pop;
RUN cd pop; make benchmarks;
