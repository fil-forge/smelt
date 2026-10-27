# Foundry plus the evm assembler that builds the ERC-8167 dispatcher FWSS runs
# on from v1.5.0. upgrade-contracts.sh builds this image with the assembler
# revision filecoin-services pins in service_contracts/Makefile (EVM_REVISION).
ARG FOUNDRY_IMAGE
FROM ${FOUNDRY_IMAGE}
ARG EVM_REVISION
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential autoconf automake libtool pkg-config \
    libcurl4-openssl-dev ca-certificates jq \
 && rm -rf /var/lib/apt/lists/*
# This image's git predates `clone --revision`, so fetch the commit directly.
RUN git init -q /opt/evm && cd /opt/evm \
 && git remote add origin https://github.com/wjmelements/evm.git \
 && git fetch -q --depth 1 origin "${EVM_REVISION}" && git checkout -q FETCH_HEAD \
 && git submodule update -q --init --recursive --depth 1 \
 && make bin/evm && install bin/evm /usr/local/bin/evm && rm -rf /opt/evm
