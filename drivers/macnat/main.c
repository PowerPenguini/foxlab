// SPDX-License-Identifier: GPL-2.0
#include "foxlab_macnat.h"

static int __init foxlab_macnat_init(void)
{
	int ret;

	ret = foxlab_register_bridge_hooks();
	if (ret)
		return ret;
	ret = foxlab_register_control();
	if (ret) {
		foxlab_unregister_bridge_hooks();
		return ret;
	}
	pr_info("foxlab_macnat: loaded controller and packet hooks /dev/%s\n",
		FOXLAB_MACNAT_DEVICE);
	return 0;
}

static void __exit foxlab_macnat_exit(void)
{
	foxlab_unregister_control();
	foxlab_unregister_all_uplink_ingress();
	foxlab_unregister_bridge_hooks();
	pr_info("foxlab_macnat: unloaded\n");
}

module_init(foxlab_macnat_init);
module_exit(foxlab_macnat_exit);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("FoxLab");
MODULE_DESCRIPTION("FoxLab experimental Wi-Fi MAC NAT");
