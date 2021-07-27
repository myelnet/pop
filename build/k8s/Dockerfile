FROM debian:latest

RUN apt-get update -y && \
    apt-get install build-essential curl wget hwloc cron git mesa-opencl-icd ocl-icd-opencl-dev gcc git bzr jq pkg-config clang libhwloc-dev -y


RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
ENV PATH="~/.cargo/bin:${PATH}"

ENV LDFLAGS="-L/usr/local/lib"
ENV CPPFLAGS="-I/usr/local/include"
ENV CPATH=/usr/local/include
ENV LIBRARY_PATH=/usr/local/lib
ENV LD_LIBRARY_PATH=/usr/local/lib
ENV CGO_CFLAGS="-D__BLST_PORTABLE__"
ENV RUSTFLAGS="-C target-cpu=native -g"
ENV FFI_BUILD_FROM_SOURCE=0

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

RUN git clone https://github.com/myelnet/pop;
RUN cd pop; make all;

COPY ./install-playbook/pop-env.sh /root/pop-env.sh
COPY ./install-playbook/key /root/key

RUN chmod +x /root/pop-env.sh

CMD . /root/pop-env.sh; echo $BOOTSTRAP; pop start -bootstrap=$BOOTSTRAP \
    -privkey=/root/key -regions=Global -capacity=$CAPACITY -maxppb=$MAXPPB -temp-repo=false
