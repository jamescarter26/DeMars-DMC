#!/usr/bin/env sh

##
## Input parameters
##
BINARY=/Demars-DMC/${BINARY:-Demars-DMC}
ID=${ID:-0}
LOG=${LOG:-Demars-DMC.log}

##
## Assert linux binary
##
if ! [ -f "${BINARY}" ]; then
	echo "The binary $(basename "${BINARY}") cannot be found. Please add the binary to the shared folder. Please use the BINARY environment variable if the name of the binary is not 'Demars-DMC' E.g.: -e BINARY=Demars-DMC_my_test_version"
	exit 1
fi
BINARY_CHECK="$(file "$BINARY" | grep 'ELF 64-bit LSB executable, x86-64')"
if [ -z "${BINARY_CHECK}" ]; then
	echo "Binary needs to be OS linux, ARCH amd64"
	exit 1
fi

##
## Run binary with all parameters
##
export TMHOME="/Demars-DMC/node${ID}"

if [ -d "`dirname ${TMHOME}/${LOG}`" ]; then
  "$BINARY" "$@" | tee "${TMHOME}/${LOG}"
else
  "$BINARY" "$@"
fi

chmod 777 -R /Demars-DMC

