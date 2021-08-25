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

RUN git clone https://github.com/myelnet/pop;
RUN cd pop; make all;

COPY ./install-playbook/pop-env.sh /root/pop-env.sh
COPY ./install-playbook/influxdb-env.sh /root/influxdb-env.sh
COPY ./install-playbook/key /root/key

RUN chmod +x /root/pop-env.sh
RUN chmod +x /root/influxdb-env.sh

CMD . /root/pop-env.sh; . /root/influxdb-env.sh; echo $BOOTSTRAP; pop start -bootstrap=$BOOTSTRAP \
    -privkey=/root/key -regions=Global -capacity=$CAPACITY -maxppb=$MAXPPB -temp-repo=false
