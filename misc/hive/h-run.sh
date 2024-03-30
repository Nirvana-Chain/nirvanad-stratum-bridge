#!/usr/bin/env bash

[[ ! -e ./config.yaml ]] && echo "missing config.yaml" && pwd && exit 1

nva_bridge  $(< nva_bridge.conf)| tee --append $CUSTOM_LOG_BASENAME.log
