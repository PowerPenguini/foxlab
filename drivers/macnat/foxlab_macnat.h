/* SPDX-License-Identifier: GPL-2.0 */
#ifndef FOXLAB_MACNAT_H
#define FOXLAB_MACNAT_H

#include <linux/etherdevice.h>
#include <linux/fs.h>
#include <linux/if_arp.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/kernel.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/mutex.h>
#include <linux/netdevice.h>
#include <linux/netfilter.h>
#include <linux/netfilter_bridge.h>
#include <linux/skbuff.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/string.h>
#include <linux/uaccess.h>
#include <linux/udp.h>
#include <asm/unaligned.h>
#include <net/checksum.h>
#include <net/net_namespace.h>
#include <uapi/linux/netfilter_ipv4.h>

#define FOXLAB_MACNAT_DEVICE "macnat"
#define FOXLAB_MACNAT_MAX_CONFIG 8192
#define FOXLAB_MACNAT_MAX_SESSIONS 16
#define FOXLAB_MACNAT_STATUS_SIZE 32768
#define FOXLAB_MACNAT_MAX_VMS 32

struct foxlab_bootp {
	u8 op;
	u8 htype;
	u8 hlen;
	u8 hops;
	__be32 xid;
	__be16 secs;
	__be16 flags;
	__be32 ciaddr;
	__be32 yiaddr;
	__be32 siaddr;
	__be32 giaddr;
	u8 chaddr[16];
	u8 sname[64];
	u8 file[128];
} __packed;

struct foxlab_vm {
	u8 mac[ETH_ALEN];
	__be32 ipv4;
	__be32 dhcp_xid;
	int ifindex;
	bool has_ipv4;
	bool has_dhcp_xid;
};

struct foxlab_stats {
	u64 tx_to_uplink;
	u64 rx_to_vm;
	u64 drops;
	u64 learned_ports;
	u64 learned_ipv4;
	u64 learned_dhcp;
	u64 arp_out;
	u64 arp_in;
	u64 dhcp_out;
	u64 dhcp_in;
	u64 ipv4_out;
	u64 ipv4_in;
	u64 ignored_wrong_bridge;
	u64 dhcp_option61_rewrite;
	u64 dhcp_checksum_rewrite;
	u64 inbound_no_vm_match;
	u64 inbound_no_learned_port;
};

struct foxlab_session {
	bool active;
	char lab_id[64];
	char switch_id[64];
	char bridge[IFNAMSIZ];
	char uplink[IFNAMSIZ];
	int bridge_ifindex;
	int uplink_ifindex;
	u8 uplink_mac[ETH_ALEN];
	unsigned int vm_count;
	struct foxlab_vm vms[FOXLAB_MACNAT_MAX_VMS];
	struct foxlab_stats stats;
};

struct foxlab_pending_session {
	char lab_id[64];
	char switch_id[64];
	char bridge[IFNAMSIZ];
	char uplink[IFNAMSIZ];
	unsigned int vm_count;
	u8 vm_macs[FOXLAB_MACNAT_MAX_VMS][ETH_ALEN];
};

extern spinlock_t session_lock;
extern struct foxlab_session sessions[FOXLAB_MACNAT_MAX_SESSIONS];

int foxlab_register_bridge_hooks(void);
void foxlab_unregister_bridge_hooks(void);
int foxlab_register_uplink_ingress(struct net_device *uplink_dev);
void foxlab_unregister_uplink_ingress(int ifindex);
void foxlab_unregister_all_uplink_ingress(void);
bool foxlab_uplink_ingress_is_registered_for(int ifindex);

int foxlab_register_control(void);
void foxlab_unregister_control(void);

#endif
