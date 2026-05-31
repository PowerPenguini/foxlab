// SPDX-License-Identifier: GPL-2.0
#include "foxlab_macnat.h"

#define FOXLAB_DHCP_MAGIC_COOKIE 0x63825363
#define FOXLAB_DHCP_OPTION_PAD 0
#define FOXLAB_DHCP_OPTION_END 255
#define FOXLAB_DHCP_OPTION_CLIENT_ID 61
#define FOXLAB_DHCP_CLIENT_ID_HTYPE_ETHER 1
#define FOXLAB_DHCP_REWRITE_CHADDR BIT(0)
#define FOXLAB_DHCP_REWRITE_OPTION61 BIT(1)
#define FOXLAB_DHCP_REWRITE_CHECKSUM BIT(2)

struct foxlab_uplink_hook {
	bool active;
	int ifindex;
	unsigned int refcount;
	struct net_device *dev;
	struct nf_hook_ops ops;
};

static DEFINE_MUTEX(uplink_hooks_lock);
static struct foxlab_uplink_hook uplink_hooks[FOXLAB_MACNAT_MAX_SESSIONS];

static unsigned int foxlab_bridge_pre(void *priv, struct sk_buff *skb,
				      const struct nf_hook_state *state);
static unsigned int foxlab_uplink_ingress(void *priv, struct sk_buff *skb,
					  const struct nf_hook_state *state);

static struct nf_hook_ops foxlab_bridge_ops = {
	.hook = foxlab_bridge_pre,
	.pf = NFPROTO_BRIDGE,
	.hooknum = NF_BR_PRE_ROUTING,
	.priority = NF_BR_PRI_FIRST,
};

int foxlab_register_bridge_hooks(void)
{
	return nf_register_net_hook(&init_net, &foxlab_bridge_ops);
}

void foxlab_unregister_bridge_hooks(void)
{
	nf_unregister_net_hook(&init_net, &foxlab_bridge_ops);
}

int foxlab_register_uplink_ingress(struct net_device *uplink_dev)
{
	struct foxlab_uplink_hook *free_hook = NULL;
	unsigned int i;
	int ret;

	mutex_lock(&uplink_hooks_lock);
	for (i = 0; i < ARRAY_SIZE(uplink_hooks); i++) {
		if (uplink_hooks[i].active &&
		    uplink_hooks[i].ifindex == uplink_dev->ifindex) {
			uplink_hooks[i].refcount++;
			mutex_unlock(&uplink_hooks_lock);
			dev_put(uplink_dev);
			return 0;
		}
		if (!uplink_hooks[i].active && !free_hook)
			free_hook = &uplink_hooks[i];
	}
	if (!free_hook) {
		mutex_unlock(&uplink_hooks_lock);
		return -ENOSPC;
	}

	memset(free_hook, 0, sizeof(*free_hook));
	free_hook->ifindex = uplink_dev->ifindex;
	free_hook->dev = uplink_dev;
	free_hook->refcount = 1;
	free_hook->ops.hook = foxlab_uplink_ingress;
	free_hook->ops.pf = NFPROTO_NETDEV;
	free_hook->ops.hooknum = NF_NETDEV_INGRESS;
	free_hook->ops.priority = NF_IP_PRI_FIRST;
	free_hook->ops.dev = uplink_dev;

	ret = nf_register_net_hook(dev_net(uplink_dev), &free_hook->ops);
	if (ret) {
		memset(free_hook, 0, sizeof(*free_hook));
		mutex_unlock(&uplink_hooks_lock);
		return ret;
	}
	free_hook->active = true;
	mutex_unlock(&uplink_hooks_lock);
	return 0;
}

void foxlab_unregister_uplink_ingress(int ifindex)
{
	unsigned int i;

	if (ifindex <= 0)
		return;
	mutex_lock(&uplink_hooks_lock);
	for (i = 0; i < ARRAY_SIZE(uplink_hooks); i++) {
		if (!uplink_hooks[i].active || uplink_hooks[i].ifindex != ifindex)
			continue;
		if (uplink_hooks[i].refcount > 1) {
			uplink_hooks[i].refcount--;
			mutex_unlock(&uplink_hooks_lock);
			return;
		}
		nf_unregister_net_hook(dev_net(uplink_hooks[i].dev),
				       &uplink_hooks[i].ops);
		dev_put(uplink_hooks[i].dev);
		memset(&uplink_hooks[i], 0, sizeof(uplink_hooks[i]));
		mutex_unlock(&uplink_hooks_lock);
		return;
	}
	mutex_unlock(&uplink_hooks_lock);
}

void foxlab_unregister_all_uplink_ingress(void)
{
	unsigned int i;

	mutex_lock(&uplink_hooks_lock);
	for (i = 0; i < ARRAY_SIZE(uplink_hooks); i++) {
		if (!uplink_hooks[i].active)
			continue;
		nf_unregister_net_hook(dev_net(uplink_hooks[i].dev),
				       &uplink_hooks[i].ops);
		dev_put(uplink_hooks[i].dev);
		memset(&uplink_hooks[i], 0, sizeof(uplink_hooks[i]));
	}
	mutex_unlock(&uplink_hooks_lock);
}

bool foxlab_uplink_ingress_is_registered_for(int ifindex)
{
	unsigned int i;
	bool found = false;

	mutex_lock(&uplink_hooks_lock);
	for (i = 0; i < ARRAY_SIZE(uplink_hooks); i++) {
		if (uplink_hooks[i].active && uplink_hooks[i].ifindex == ifindex) {
			found = true;
			break;
		}
	}
	mutex_unlock(&uplink_hooks_lock);
	return found;
}

static int foxlab_find_vm_by_mac_locked(struct foxlab_session *session,
					const u8 *mac)
{
	unsigned int i;

	for (i = 0; i < session->vm_count; i++) {
		if (ether_addr_equal(session->vms[i].mac, mac))
			return i;
	}
	return -1;
}

static int foxlab_find_vm_by_ipv4_locked(struct foxlab_session *session,
					 __be32 ipv4)
{
	unsigned int i;

	if (ipv4 == 0)
		return -1;
	for (i = 0; i < session->vm_count; i++) {
		if (session->vms[i].has_ipv4 && session->vms[i].ipv4 == ipv4)
			return i;
	}
	return -1;
}

static int foxlab_find_vm_by_xid_locked(struct foxlab_session *session,
					__be32 xid)
{
	unsigned int i;

	for (i = 0; i < session->vm_count; i++) {
		if (session->vms[i].has_dhcp_xid &&
		    session->vms[i].dhcp_xid == xid)
			return i;
	}
	return -1;
}

static int foxlab_find_session_by_bridge_locked(int bridge_ifindex)
{
	unsigned int i;

	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (sessions[i].active &&
		    sessions[i].bridge_ifindex == bridge_ifindex)
			return i;
	}
	return -1;
}

static bool foxlab_session_matches_uplink(const struct foxlab_session *session,
					  int uplink_ifindex)
{
	return session->active && session->uplink_ifindex == uplink_ifindex;
}

static bool foxlab_get_uplink_mac_locked(int uplink_ifindex, u8 *uplink_mac)
{
	unsigned int i;

	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (!foxlab_session_matches_uplink(&sessions[i], uplink_ifindex))
			continue;
		ether_addr_copy(uplink_mac, sessions[i].uplink_mac);
		return true;
	}
	return false;
}

static void foxlab_count_wrong_bridge_locked(void)
{
	unsigned int i;

	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (sessions[i].active)
			sessions[i].stats.ignored_wrong_bridge++;
	}
}

static void foxlab_count_drop(int session_index)
{
	unsigned long flags;

	if (session_index < 0 || session_index >= FOXLAB_MACNAT_MAX_SESSIONS)
		return;
	spin_lock_irqsave(&session_lock, flags);
	if (sessions[session_index].active)
		sessions[session_index].stats.drops++;
	spin_unlock_irqrestore(&session_lock, flags);
}

static void foxlab_count_uplink_drop(int uplink_ifindex)
{
	unsigned long flags;
	unsigned int i;

	spin_lock_irqsave(&session_lock, flags);
	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (foxlab_session_matches_uplink(&sessions[i], uplink_ifindex))
			sessions[i].stats.drops++;
	}
	spin_unlock_irqrestore(&session_lock, flags);
}

static struct sk_buff *foxlab_copy_for_xmit(struct sk_buff *skb)
{
	struct sk_buff *nskb;
	unsigned int push_len;

	nskb = skb_copy(skb, GFP_ATOMIC);
	if (!nskb)
		return NULL;
	if (!skb_mac_header_was_set(nskb))
		skb_reset_mac_header(nskb);
	if (skb_mac_header(nskb) < nskb->data) {
		push_len = nskb->data - skb_mac_header(nskb);
		skb_push(nskb, push_len);
	}
	skb_reset_mac_header(nskb);
	if (!pskb_may_pull(nskb, ETH_HLEN)) {
		kfree_skb(nskb);
		return NULL;
	}
	return nskb;
}

static bool foxlab_pull_l2(struct sk_buff *skb, unsigned int len)
{
	if (len < ETH_HLEN)
		len = ETH_HLEN;
	return pskb_may_pull(skb, len);
}

static void foxlab_count_dhcp_rewrite(int session_index,
				      unsigned int rewrite_flags)
{
	unsigned long flags;

	if (!rewrite_flags || session_index < 0 ||
	    session_index >= FOXLAB_MACNAT_MAX_SESSIONS)
		return;
	spin_lock_irqsave(&session_lock, flags);
	if (sessions[session_index].active) {
		if (rewrite_flags & FOXLAB_DHCP_REWRITE_OPTION61)
			sessions[session_index].stats.dhcp_option61_rewrite++;
		if (rewrite_flags & FOXLAB_DHCP_REWRITE_CHECKSUM)
			sessions[session_index].stats.dhcp_checksum_rewrite++;
	}
	spin_unlock_irqrestore(&session_lock, flags);
}

static int foxlab_port_bridge_ifindex(const struct net_device *dev)
{
	const struct net_device *master;
	int ifindex;

	if (!dev)
		return 0;
	ifindex = dev->ifindex;
	rcu_read_lock();
	master = netdev_master_upper_dev_get_rcu((struct net_device *)dev);
	if (master)
		ifindex = master->ifindex;
	rcu_read_unlock();
	return ifindex;
}

static void foxlab_rewrite_arp_sender(struct sk_buff *skb, const u8 *mac)
{
	struct ethhdr *eth = (struct ethhdr *)skb->data;
	struct arphdr *arp;
	unsigned char *payload;

	if (ntohs(eth->h_proto) != ETH_P_ARP ||
	    !foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp)))
		return;
	eth = (struct ethhdr *)skb->data;
	arp = (struct arphdr *)(eth + 1);
	if (arp->ar_hrd != htons(ARPHRD_ETHER) ||
	    arp->ar_pro != htons(ETH_P_IP) || arp->ar_hln != ETH_ALEN ||
	    arp->ar_pln != 4 ||
	    !foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp) + 20))
		return;
	eth = (struct ethhdr *)skb->data;
	arp = (struct arphdr *)(eth + 1);
	payload = (unsigned char *)(arp + 1);
	ether_addr_copy(payload, mac);
}

static void foxlab_rewrite_arp_target(struct sk_buff *skb, const u8 *mac)
{
	struct ethhdr *eth = (struct ethhdr *)skb->data;
	struct arphdr *arp;
	unsigned char *payload;

	if (ntohs(eth->h_proto) != ETH_P_ARP ||
	    !foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp)))
		return;
	eth = (struct ethhdr *)skb->data;
	arp = (struct arphdr *)(eth + 1);
	if (arp->ar_hrd != htons(ARPHRD_ETHER) ||
	    arp->ar_pro != htons(ETH_P_IP) || arp->ar_hln != ETH_ALEN ||
	    arp->ar_pln != 4 ||
	    !foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp) + 20))
		return;
	eth = (struct ethhdr *)skb->data;
	arp = (struct arphdr *)(eth + 1);
	payload = (unsigned char *)(arp + 1);
	ether_addr_copy(payload + ETH_ALEN + 4, mac);
}

static bool foxlab_rewrite_dhcp_option61(struct foxlab_bootp *bootp,
					 unsigned int payload_len,
					 const u8 *mac)
{
	u8 *cursor;
	unsigned int remaining;

	if (payload_len < sizeof(*bootp) + 4)
		return false;
	cursor = (u8 *)bootp + sizeof(*bootp);
	if (get_unaligned_be32(cursor) != FOXLAB_DHCP_MAGIC_COOKIE)
		return false;
	cursor += 4;
	remaining = payload_len - sizeof(*bootp) - 4;

	while (remaining > 0) {
		u8 code = *cursor++;
		u8 option_len;

		remaining--;
		if (code == FOXLAB_DHCP_OPTION_PAD)
			continue;
		if (code == FOXLAB_DHCP_OPTION_END)
			break;
		if (remaining < 1)
			break;
		option_len = *cursor++;
		remaining--;
		if (option_len > remaining)
			break;
		if (code == FOXLAB_DHCP_OPTION_CLIENT_ID &&
		    option_len == ETH_ALEN + 1 &&
		    cursor[0] == FOXLAB_DHCP_CLIENT_ID_HTYPE_ETHER) {
			if (ether_addr_equal(cursor + 1, mac))
				return false;
			ether_addr_copy(cursor + 1, mac);
			return true;
		}
		cursor += option_len;
		remaining -= option_len;
	}
	return false;
}

static void foxlab_update_udp_checksum(struct sk_buff *skb, struct iphdr *ip,
				       struct udphdr *udp, unsigned int udp_len)
{
	udp->check = 0;
	udp->check = csum_tcpudp_magic(ip->saddr, ip->daddr, udp_len,
				       IPPROTO_UDP,
				       csum_partial(udp, udp_len, 0));
	if (udp->check == 0)
		udp->check = CSUM_MANGLED_0;
	skb->ip_summed = CHECKSUM_NONE;
}

static unsigned int foxlab_rewrite_dhcp_identity(struct sk_buff *skb,
						 const u8 *mac)
{
	struct ethhdr *eth = (struct ethhdr *)skb->data;
	struct iphdr *ip;
	struct udphdr *udp;
	struct foxlab_bootp *bootp;
	unsigned int l4_offset;
	unsigned int udp_len;
	unsigned int payload_len;
	unsigned int rewrite_flags = 0;

	if (ntohs(eth->h_proto) != ETH_P_IP ||
	    !foxlab_pull_l2(skb, ETH_HLEN + sizeof(*ip)))
		return 0;
	eth = (struct ethhdr *)skb->data;
	ip = (struct iphdr *)(eth + 1);
	if (ip->version != 4 || ip->ihl < 5 || ip->protocol != IPPROTO_UDP)
		return 0;
	l4_offset = ETH_HLEN + (ip->ihl * 4);
	if (!foxlab_pull_l2(skb, l4_offset + sizeof(*udp) + sizeof(*bootp)))
		return 0;
	eth = (struct ethhdr *)skb->data;
	ip = (struct iphdr *)(eth + 1);
	udp = (struct udphdr *)((u8 *)ip + (ip->ihl * 4));
	if (!((udp->source == htons(67) && udp->dest == htons(68)) ||
	      (udp->source == htons(68) && udp->dest == htons(67))))
		return 0;
	udp_len = ntohs(udp->len);
	if (udp_len < sizeof(*udp) + sizeof(*bootp))
		return 0;
	if (!foxlab_pull_l2(skb, l4_offset + udp_len))
		return 0;
	eth = (struct ethhdr *)skb->data;
	ip = (struct iphdr *)(eth + 1);
	udp = (struct udphdr *)((u8 *)ip + (ip->ihl * 4));
	bootp = (struct foxlab_bootp *)(udp + 1);
	if (bootp->htype != ARPHRD_ETHER || bootp->hlen != ETH_ALEN)
		return 0;
	if (!ether_addr_equal(bootp->chaddr, mac)) {
		ether_addr_copy(bootp->chaddr, mac);
		rewrite_flags |= FOXLAB_DHCP_REWRITE_CHADDR;
	}
	payload_len = udp_len - sizeof(*udp);
	if (foxlab_rewrite_dhcp_option61(bootp, payload_len, mac))
		rewrite_flags |= FOXLAB_DHCP_REWRITE_OPTION61;
	if (rewrite_flags) {
		foxlab_update_udp_checksum(skb, ip, udp, udp_len);
		rewrite_flags |= FOXLAB_DHCP_REWRITE_CHECKSUM;
	}
	return rewrite_flags;
}

static void foxlab_learn_outbound(struct sk_buff *skb, int session_index,
				  int vm_index, int in_ifindex)
{
	struct ethhdr *eth = (struct ethhdr *)skb->data;
	unsigned long flags;
	struct foxlab_session *session;

	if (session_index < 0 || session_index >= FOXLAB_MACNAT_MAX_SESSIONS)
		return;
	spin_lock_irqsave(&session_lock, flags);
	session = &sessions[session_index];
	if (!session->active || vm_index < 0 || vm_index >= session->vm_count) {
		spin_unlock_irqrestore(&session_lock, flags);
		return;
	}
	if (in_ifindex > 0 && session->vms[vm_index].ifindex != in_ifindex) {
		session->vms[vm_index].ifindex = in_ifindex;
		session->stats.learned_ports++;
	}
	if (ntohs(eth->h_proto) == ETH_P_ARP) {
		struct arphdr *arp;
		unsigned char *payload;

		session->stats.arp_out++;
		spin_unlock_irqrestore(&session_lock, flags);
		if (!foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp) + 20))
			return;
		eth = (struct ethhdr *)skb->data;
		arp = (struct arphdr *)(eth + 1);
		if (arp->ar_hrd != htons(ARPHRD_ETHER) ||
		    arp->ar_pro != htons(ETH_P_IP) || arp->ar_hln != ETH_ALEN ||
		    arp->ar_pln != 4)
			return;
		payload = (unsigned char *)(arp + 1);
		spin_lock_irqsave(&session_lock, flags);
		session = &sessions[session_index];
		if (!session->active || vm_index < 0 ||
		    vm_index >= session->vm_count) {
			spin_unlock_irqrestore(&session_lock, flags);
			return;
		}
		if (*(__be32 *)(payload + ETH_ALEN) != 0) {
			session->vms[vm_index].ipv4 =
				*(__be32 *)(payload + ETH_ALEN);
			session->vms[vm_index].has_ipv4 = true;
			session->stats.learned_ipv4++;
		}
		spin_unlock_irqrestore(&session_lock, flags);
		return;
	}
	if (ntohs(eth->h_proto) == ETH_P_IP) {
		struct iphdr *ip;

		session->stats.ipv4_out++;
		spin_unlock_irqrestore(&session_lock, flags);
		if (!foxlab_pull_l2(skb, ETH_HLEN + sizeof(*ip)))
			return;
		eth = (struct ethhdr *)skb->data;
		ip = (struct iphdr *)(eth + 1);
		if (ip->version != 4 || ip->ihl < 5)
			return;
		spin_lock_irqsave(&session_lock, flags);
		session = &sessions[session_index];
		if (!session->active || vm_index < 0 ||
		    vm_index >= session->vm_count) {
			spin_unlock_irqrestore(&session_lock, flags);
			return;
		}
		if (ip->saddr != 0) {
			session->vms[vm_index].ipv4 = ip->saddr;
			session->vms[vm_index].has_ipv4 = true;
			session->stats.learned_ipv4++;
		}
		if (ip->protocol == IPPROTO_UDP) {
			struct udphdr *udp;
			struct foxlab_bootp *bootp;
			unsigned int l4_offset = ETH_HLEN + (ip->ihl * 4);

			spin_unlock_irqrestore(&session_lock, flags);
			if (!foxlab_pull_l2(skb, l4_offset + sizeof(*udp) +
						     sizeof(*bootp)))
				return;
			eth = (struct ethhdr *)skb->data;
			ip = (struct iphdr *)(eth + 1);
			udp = (struct udphdr *)((u8 *)ip + (ip->ihl * 4));
			if (udp->dest != htons(67) && udp->dest != htons(68))
				return;
			bootp = (struct foxlab_bootp *)(udp + 1);
			spin_lock_irqsave(&session_lock, flags);
			session = &sessions[session_index];
			if (!session->active || vm_index < 0 ||
			    vm_index >= session->vm_count) {
				spin_unlock_irqrestore(&session_lock, flags);
				return;
			}
			session->vms[vm_index].dhcp_xid = bootp->xid;
			session->vms[vm_index].has_dhcp_xid = true;
			session->stats.learned_dhcp++;
			session->stats.dhcp_out++;
			spin_unlock_irqrestore(&session_lock, flags);
			return;
		}
		spin_unlock_irqrestore(&session_lock, flags);
		return;
	}
	spin_unlock_irqrestore(&session_lock, flags);
}

static bool foxlab_select_inbound_vm(struct sk_buff *skb, int uplink_ifindex,
				     int *session_index, int *vm_ifindex,
				     u8 *vm_mac)
{
	struct ethhdr *eth = (struct ethhdr *)skb->data;
	unsigned long flags;
	int selected_session = -1;
	int selected_vm = -1;
	bool has_ipv4 = false;
	bool has_arp = false;
	bool has_xid = false;
	__be32 ipv4 = 0;
	__be32 xid = 0;
	unsigned int i;

	if (ntohs(eth->h_proto) == ETH_P_ARP) {
		struct arphdr *arp;
		unsigned char *payload;

		if (!foxlab_pull_l2(skb, ETH_HLEN + sizeof(*arp) + 20))
			return false;
		eth = (struct ethhdr *)skb->data;
		arp = (struct arphdr *)(eth + 1);
		if (arp->ar_hrd != htons(ARPHRD_ETHER) ||
		    arp->ar_pro != htons(ETH_P_IP) || arp->ar_hln != ETH_ALEN ||
		    arp->ar_pln != 4)
			return false;
		payload = (unsigned char *)(arp + 1);
		ipv4 = *(__be32 *)(payload + ETH_ALEN + 4 + ETH_ALEN);
		has_arp = true;
		has_ipv4 = true;
	} else if (ntohs(eth->h_proto) == ETH_P_IP) {
		struct iphdr *ip;

		if (!foxlab_pull_l2(skb, ETH_HLEN + sizeof(*ip)))
			return false;
		eth = (struct ethhdr *)skb->data;
		ip = (struct iphdr *)(eth + 1);
		if (ip->version != 4 || ip->ihl < 5)
			return false;
		ipv4 = ip->daddr;
		has_ipv4 = true;
		if (ip->protocol == IPPROTO_UDP) {
			struct udphdr *udp;
			struct foxlab_bootp *bootp;
			unsigned int l4_offset = ETH_HLEN + (ip->ihl * 4);

			if (foxlab_pull_l2(skb, l4_offset + sizeof(*udp) +
							sizeof(*bootp))) {
				eth = (struct ethhdr *)skb->data;
				ip = (struct iphdr *)(eth + 1);
				udp = (struct udphdr *)((u8 *)ip +
							 (ip->ihl * 4));
				if (udp->source == htons(67) ||
				    udp->source == htons(68)) {
					bootp = (struct foxlab_bootp *)(udp + 1);
					xid = bootp->xid;
					has_xid = true;
				}
			}
		}
	} else {
		return false;
	}

	spin_lock_irqsave(&session_lock, flags);
	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		struct foxlab_session *session = &sessions[i];
		int vm_index = -1;

		if (!foxlab_session_matches_uplink(session, uplink_ifindex))
			continue;
		if (has_arp)
			session->stats.arp_in++;
		else if (has_ipv4)
			session->stats.ipv4_in++;
		if (has_ipv4)
			vm_index = foxlab_find_vm_by_ipv4_locked(session, ipv4);
		if (vm_index < 0 && has_xid) {
			session->stats.dhcp_in++;
			vm_index = foxlab_find_vm_by_xid_locked(session, xid);
		}
		if (vm_index >= 0) {
			selected_session = i;
			selected_vm = vm_index;
			break;
		}
	}

	if (selected_session < 0) {
		for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
			if (foxlab_session_matches_uplink(&sessions[i],
							  uplink_ifindex))
				sessions[i].stats.inbound_no_vm_match++;
		}
		spin_unlock_irqrestore(&session_lock, flags);
		return false;
	}
	if (sessions[selected_session].vms[selected_vm].ifindex <= 0) {
		sessions[selected_session].stats.inbound_no_learned_port++;
		spin_unlock_irqrestore(&session_lock, flags);
		return false;
	}
	*session_index = selected_session;
	*vm_ifindex = sessions[selected_session].vms[selected_vm].ifindex;
	ether_addr_copy(vm_mac, sessions[selected_session].vms[selected_vm].mac);
	spin_unlock_irqrestore(&session_lock, flags);
	return true;
}

static unsigned int foxlab_bridge_pre(void *priv, struct sk_buff *skb,
				      const struct nf_hook_state *state)
{
	struct ethhdr *eth;
	struct sk_buff *nskb;
	struct net_device *uplink_dev;
	unsigned long flags;
	u8 uplink_mac[ETH_ALEN];
	int vm_index;
	int session_index;
	int uplink_ifindex;
	int in_ifindex = state->in ? state->in->ifindex : 0;
	int bridge_ifindex;
	unsigned int rewrite_flags;

	if (!skb || !skb_mac_header_was_set(skb))
		return NF_ACCEPT;
	eth = eth_hdr(skb);
	if (!eth)
		return NF_ACCEPT;

	bridge_ifindex = foxlab_port_bridge_ifindex(state->in);
	if (bridge_ifindex <= 0)
		return NF_ACCEPT;

	spin_lock_irqsave(&session_lock, flags);
	session_index = foxlab_find_session_by_bridge_locked(bridge_ifindex);
	if (session_index < 0) {
		foxlab_count_wrong_bridge_locked();
		spin_unlock_irqrestore(&session_lock, flags);
		return NF_ACCEPT;
	}
	vm_index = foxlab_find_vm_by_mac_locked(&sessions[session_index],
						eth->h_source);
	if (vm_index < 0) {
		spin_unlock_irqrestore(&session_lock, flags);
		return NF_ACCEPT;
	}
	uplink_ifindex = sessions[session_index].uplink_ifindex;
	ether_addr_copy(uplink_mac, sessions[session_index].uplink_mac);
	spin_unlock_irqrestore(&session_lock, flags);

	nskb = foxlab_copy_for_xmit(skb);
	if (!nskb) {
		foxlab_count_drop(session_index);
		return NF_ACCEPT;
	}
	foxlab_learn_outbound(nskb, session_index, vm_index, in_ifindex);
	eth = (struct ethhdr *)nskb->data;
	ether_addr_copy(eth->h_source, uplink_mac);
	foxlab_rewrite_arp_sender(nskb, uplink_mac);
	rewrite_flags = foxlab_rewrite_dhcp_identity(nskb, uplink_mac);
	foxlab_count_dhcp_rewrite(session_index, rewrite_flags);

	uplink_dev = dev_get_by_index(&init_net, uplink_ifindex);
	if (!uplink_dev) {
		kfree_skb(nskb);
		foxlab_count_drop(session_index);
		return NF_ACCEPT;
	}
	nskb->dev = uplink_dev;
	dev_queue_xmit(nskb);
	dev_put(uplink_dev);
	spin_lock_irqsave(&session_lock, flags);
	if (sessions[session_index].active)
		sessions[session_index].stats.tx_to_uplink++;
	spin_unlock_irqrestore(&session_lock, flags);
	return NF_ACCEPT;
}

static unsigned int foxlab_uplink_ingress(void *priv, struct sk_buff *skb,
					  const struct nf_hook_state *state)
{
	struct ethhdr *eth;
	struct sk_buff *nskb;
	struct net_device *vm_dev;
	unsigned long flags;
	u8 vm_mac[ETH_ALEN];
	u8 uplink_mac[ETH_ALEN];
	int vm_ifindex = 0;
	int session_index = -1;
	int uplink_ifindex;
	unsigned int rewrite_flags;

	if (!skb || !state->in)
		return NF_ACCEPT;
	uplink_ifindex = state->in->ifindex;
	spin_lock_irqsave(&session_lock, flags);
	if (!foxlab_get_uplink_mac_locked(uplink_ifindex, uplink_mac)) {
		spin_unlock_irqrestore(&session_lock, flags);
		return NF_ACCEPT;
	}
	spin_unlock_irqrestore(&session_lock, flags);

	nskb = foxlab_copy_for_xmit(skb);
	if (!nskb) {
		foxlab_count_uplink_drop(uplink_ifindex);
		return NF_ACCEPT;
	}
	eth = (struct ethhdr *)nskb->data;
	if (!is_multicast_ether_addr(eth->h_dest) &&
	    !ether_addr_equal(eth->h_dest, uplink_mac)) {
		kfree_skb(nskb);
		return NF_ACCEPT;
	}
	if (!foxlab_select_inbound_vm(nskb, uplink_ifindex, &session_index,
				      &vm_ifindex, vm_mac)) {
		kfree_skb(nskb);
		return NF_ACCEPT;
	}
	eth = (struct ethhdr *)nskb->data;
	ether_addr_copy(eth->h_dest, vm_mac);
	foxlab_rewrite_arp_target(nskb, vm_mac);
	rewrite_flags = foxlab_rewrite_dhcp_identity(nskb, vm_mac);
	foxlab_count_dhcp_rewrite(session_index, rewrite_flags);

	vm_dev = dev_get_by_index(&init_net, vm_ifindex);
	if (!vm_dev) {
		kfree_skb(nskb);
		foxlab_count_drop(session_index);
		return NF_ACCEPT;
	}
	nskb->dev = vm_dev;
	dev_queue_xmit(nskb);
	dev_put(vm_dev);
	spin_lock_irqsave(&session_lock, flags);
	if (session_index >= 0 &&
	    session_index < FOXLAB_MACNAT_MAX_SESSIONS &&
	    sessions[session_index].active)
		sessions[session_index].stats.rx_to_vm++;
	spin_unlock_irqrestore(&session_lock, flags);
	return NF_ACCEPT;
}
