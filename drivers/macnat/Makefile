obj-m += foxlab_macnat.o
foxlab_macnat-y := main.o control.o hooks.o state.o

KDIR ?= /lib/modules/$(shell uname -r)/build
SRC_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
BUILD_DIR ?= $(abspath $(SRC_DIR)/../../build/macnat)
SOURCES := foxlab_macnat.h main.c control.c hooks.c state.c

.PHONY: all clean

all:
	mkdir -p $(BUILD_DIR)
	cp $(addprefix $(SRC_DIR)/,$(SOURCES)) $(BUILD_DIR)/
	printf '%s\n' 'obj-m += foxlab_macnat.o' \
		'foxlab_macnat-y := main.o control.o hooks.o state.o' \
		> $(BUILD_DIR)/Makefile
	$(MAKE) -C $(KDIR) M=$(BUILD_DIR) modules

clean:
	rm -rf $(BUILD_DIR)
