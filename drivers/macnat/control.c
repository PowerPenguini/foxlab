// SPDX-License-Identifier: GPL-2.0
#include "foxlab_macnat.h"

struct foxlab_clear_request {
	char lab_id[64];
	char switch_id[64];
};

static DEFINE_MUTEX(config_lock);
static char last_config[FOXLAB_MACNAT_MAX_CONFIG];
static size_t last_config_len;

static bool foxlab_token_value(char *token, const char *key, char **value)
{
	size_t key_len = strlen(key);

	if (strncmp(token, key, key_len) != 0 || token[key_len] != '=')
		return false;
	*value = token + key_len + 1;
	return true;
}

static int foxlab_copy_field(char *dst, size_t dst_len, const char *value)
{
	if (!value || value[0] == '\0' || strnlen(value, dst_len) >= dst_len)
		return -EINVAL;
	strscpy(dst, value, dst_len);
	return 0;
}

static int foxlab_parse_configure(char *cmd, struct foxlab_pending_session *out)
{
	char *cursor = cmd;
	char *token;

	memset(out, 0, sizeof(*out));
	while ((token = strsep(&cursor, " \t\r\n")) != NULL) {
		char *value;

		if (token[0] == '\0' || strcmp(token, "configure") == 0)
			continue;
		if (foxlab_token_value(token, "labID", &value)) {
			if (foxlab_copy_field(out->lab_id, sizeof(out->lab_id),
					      value))
				return -EINVAL;
			continue;
		}
		if (foxlab_token_value(token, "switchID", &value)) {
			if (foxlab_copy_field(out->switch_id,
					      sizeof(out->switch_id), value))
				return -EINVAL;
			continue;
		}
		if (foxlab_token_value(token, "bridge", &value)) {
			if (foxlab_copy_field(out->bridge, sizeof(out->bridge),
					      value))
				return -EINVAL;
			continue;
		}
		if (foxlab_token_value(token, "uplink", &value)) {
			if (foxlab_copy_field(out->uplink, sizeof(out->uplink),
					      value))
				return -EINVAL;
			continue;
		}
		if (foxlab_token_value(token, "mac", &value)) {
			if (out->vm_count >= FOXLAB_MACNAT_MAX_VMS)
				return -E2BIG;
			if (!mac_pton(value, out->vm_macs[out->vm_count]))
				return -EINVAL;
			out->vm_count++;
			continue;
		}
		return -EINVAL;
	}
	if (out->lab_id[0] == '\0' || out->switch_id[0] == '\0' ||
	    out->bridge[0] == '\0' || out->uplink[0] == '\0')
		return -EINVAL;
	return 0;
}

static int foxlab_parse_clear(char *cmd, struct foxlab_clear_request *out)
{
	char *cursor = cmd;
	char *token;

	memset(out, 0, sizeof(*out));
	while ((token = strsep(&cursor, " \t\r\n")) != NULL) {
		char *value;

		if (token[0] == '\0' || strcmp(token, "clear") == 0)
			continue;
		if (foxlab_token_value(token, "labID", &value)) {
			if (foxlab_copy_field(out->lab_id, sizeof(out->lab_id),
					      value))
				return -EINVAL;
			continue;
		}
		if (foxlab_token_value(token, "switchID", &value)) {
			if (foxlab_copy_field(out->switch_id,
					      sizeof(out->switch_id), value))
				return -EINVAL;
			continue;
		}
		return -EINVAL;
	}
	if (out->lab_id[0] == '\0')
		return -EINVAL;
	return 0;
}

static int foxlab_find_config_slot_locked(const char *lab_id,
					  const char *switch_id)
{
	int free_slot = -1;
	unsigned int i;

	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (!sessions[i].active) {
			if (free_slot < 0)
				free_slot = i;
			continue;
		}
		if (strcmp(sessions[i].lab_id, lab_id) == 0 &&
		    strcmp(sessions[i].switch_id, switch_id) == 0)
			return i;
	}
	return free_slot;
}

static bool foxlab_pending_has_duplicate_macs(struct foxlab_pending_session *p)
{
	unsigned int i;
	unsigned int j;

	for (i = 0; i < p->vm_count; i++) {
		for (j = i + 1; j < p->vm_count; j++) {
			if (ether_addr_equal(p->vm_macs[i], p->vm_macs[j]))
				return true;
		}
	}
	return false;
}

static bool foxlab_mac_exists_elsewhere_locked(int slot,
					       struct foxlab_pending_session *p)
{
	unsigned int i;
	unsigned int j;
	unsigned int k;

	for (i = 0; i < p->vm_count; i++) {
		for (j = 0; j < FOXLAB_MACNAT_MAX_SESSIONS; j++) {
			if ((int)j == slot || !sessions[j].active)
				continue;
			for (k = 0; k < sessions[j].vm_count; k++) {
				if (ether_addr_equal(p->vm_macs[i],
						     sessions[j].vms[k].mac))
					return true;
			}
		}
	}
	return false;
}

static int foxlab_configure_session(struct foxlab_pending_session *pending)
{
	struct net_device *bridge_dev;
	struct net_device *uplink_dev;
	unsigned long flags;
	u8 uplink_mac[ETH_ALEN];
	int old_uplink_ifindex = 0;
	int new_uplink_ifindex;
	int slot;
	int ret;
	bool needs_uplink_register;
	unsigned int i;

	if (foxlab_pending_has_duplicate_macs(pending))
		return -EEXIST;

	bridge_dev = dev_get_by_name(&init_net, pending->bridge);
	if (!bridge_dev)
		return -ENODEV;
	uplink_dev = dev_get_by_name(&init_net, pending->uplink);
	if (!uplink_dev) {
		dev_put(bridge_dev);
		return -ENODEV;
	}

	spin_lock_irqsave(&session_lock, flags);
	slot = foxlab_find_config_slot_locked(pending->lab_id,
					      pending->switch_id);
	if (slot < 0) {
		spin_unlock_irqrestore(&session_lock, flags);
		dev_put(uplink_dev);
		dev_put(bridge_dev);
		return -ENOSPC;
	}
	if (foxlab_mac_exists_elsewhere_locked(slot, pending)) {
		spin_unlock_irqrestore(&session_lock, flags);
		dev_put(uplink_dev);
		dev_put(bridge_dev);
		return -EEXIST;
	}
	if (sessions[slot].active)
		old_uplink_ifindex = sessions[slot].uplink_ifindex;
	spin_unlock_irqrestore(&session_lock, flags);

	new_uplink_ifindex = uplink_dev->ifindex;
	ether_addr_copy(uplink_mac, uplink_dev->dev_addr);
	needs_uplink_register =
		old_uplink_ifindex != new_uplink_ifindex ||
		!foxlab_uplink_ingress_is_registered_for(new_uplink_ifindex);
	if (needs_uplink_register) {
		ret = foxlab_register_uplink_ingress(uplink_dev);
		if (ret) {
			dev_put(uplink_dev);
			dev_put(bridge_dev);
			return ret;
		}
	}

	spin_lock_irqsave(&session_lock, flags);
	memset(&sessions[slot], 0, sizeof(sessions[slot]));
	sessions[slot].active = true;
	strscpy(sessions[slot].lab_id, pending->lab_id,
		sizeof(sessions[slot].lab_id));
	strscpy(sessions[slot].switch_id, pending->switch_id,
		sizeof(sessions[slot].switch_id));
	strscpy(sessions[slot].bridge, pending->bridge,
		sizeof(sessions[slot].bridge));
	strscpy(sessions[slot].uplink, pending->uplink,
		sizeof(sessions[slot].uplink));
	sessions[slot].bridge_ifindex = bridge_dev->ifindex;
	sessions[slot].uplink_ifindex = new_uplink_ifindex;
	ether_addr_copy(sessions[slot].uplink_mac, uplink_mac);
	sessions[slot].vm_count = pending->vm_count;
	for (i = 0; i < pending->vm_count; i++)
		ether_addr_copy(sessions[slot].vms[i].mac, pending->vm_macs[i]);
	spin_unlock_irqrestore(&session_lock, flags);

	if (!needs_uplink_register)
		dev_put(uplink_dev);
	dev_put(bridge_dev);
	if (old_uplink_ifindex > 0 && old_uplink_ifindex != new_uplink_ifindex)
		foxlab_unregister_uplink_ingress(old_uplink_ifindex);
	pr_info("foxlab_macnat: configured lab=%s switch=%s bridge=%s uplink=%s vms=%u\n",
		pending->lab_id, pending->switch_id, pending->bridge,
		pending->uplink, pending->vm_count);
	return 0;
}

static void foxlab_clear_sessions(struct foxlab_clear_request *request)
{
	int uplinks[FOXLAB_MACNAT_MAX_SESSIONS];
	unsigned int uplink_count = 0;
	unsigned int cleared = 0;
	unsigned long flags;
	unsigned int i;

	spin_lock_irqsave(&session_lock, flags);
	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (!sessions[i].active)
			continue;
		if (strcmp(sessions[i].lab_id, request->lab_id) != 0)
			continue;
		if (request->switch_id[0] != '\0' &&
		    strcmp(sessions[i].switch_id, request->switch_id) != 0)
			continue;
		uplinks[uplink_count++] = sessions[i].uplink_ifindex;
		memset(&sessions[i], 0, sizeof(sessions[i]));
		cleared++;
	}
	spin_unlock_irqrestore(&session_lock, flags);

	for (i = 0; i < uplink_count; i++)
		foxlab_unregister_uplink_ingress(uplinks[i]);
	pr_info("foxlab_macnat: cleared %u session(s) for lab=%s switch=%s\n",
		cleared, request->lab_id,
		request->switch_id[0] ? request->switch_id : "*");
}

static const char *foxlab_session_state(const struct foxlab_session *session,
					bool ingress, const char **message)
{
	if (!session->active) {
		*message = "inactive session slot";
		return "inactive";
	}
	if (session->vm_count == 0) {
		*message = "no VM MAC addresses configured";
		return "degraded";
	}
	if (!ingress) {
		*message = "uplink ingress hook is not registered";
		return "degraded";
	}
	*message = "packet path active";
	return "active";
}

static ssize_t foxlab_macnat_read(struct file *file, char __user *buf,
				  size_t count, loff_t *ppos)
{
	struct foxlab_session *snap;
	bool ingress[FOXLAB_MACNAT_MAX_SESSIONS] = {};
	char *status;
	const char *state = "inactive";
	const char *message = "no active macnat session";
	int len = 0;
	ssize_t ret;
	size_t config_len;
	unsigned long flags;
	unsigned int session_count = 0;
	unsigned int written_sessions = 0;
	bool degraded = false;
	unsigned int i;

	snap = kcalloc(FOXLAB_MACNAT_MAX_SESSIONS, sizeof(*snap), GFP_KERNEL);
	if (!snap)
		return -ENOMEM;
	spin_lock_irqsave(&session_lock, flags);
	memcpy(snap, sessions, sizeof(*snap) * FOXLAB_MACNAT_MAX_SESSIONS);
	spin_unlock_irqrestore(&session_lock, flags);
	mutex_lock(&config_lock);
	config_len = last_config_len;
	mutex_unlock(&config_lock);

	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		if (!snap[i].active)
			continue;
		session_count++;
		ingress[i] =
			foxlab_uplink_ingress_is_registered_for(snap[i].uplink_ifindex);
		if (snap[i].vm_count == 0 || !ingress[i])
			degraded = true;
	}
	if (session_count > 0) {
		state = degraded ? "degraded" : "active";
		message = degraded ? "one or more macnat sessions are degraded" :
				     "packet path active";
	}

	status = kmalloc(FOXLAB_MACNAT_STATUS_SIZE, GFP_KERNEL);
	if (!status) {
		kfree(snap);
		return -ENOMEM;
	}
	len += scnprintf(status + len, FOXLAB_MACNAT_STATUS_SIZE - len,
			 "{\"state\":\"%s\",\"message\":\"%s\",\"sessionCount\":%u,\"sessions\":[",
			 state, message, session_count);
	for (i = 0; i < FOXLAB_MACNAT_MAX_SESSIONS; i++) {
		const char *session_state;
		const char *session_message;

		if (!snap[i].active)
			continue;
		session_state = foxlab_session_state(&snap[i], ingress[i],
						     &session_message);
		if (len < FOXLAB_MACNAT_STATUS_SIZE)
			len += scnprintf(status + len,
					 FOXLAB_MACNAT_STATUS_SIZE - len,
					 "%s{\"state\":\"%s\",\"message\":\"%s\",\"labID\":\"%s\",\"switchID\":\"%s\",\"bridge\":\"%s\",\"uplink\":\"%s\",\"vmCount\":%u,\"txToUplink\":%llu,\"rxToVM\":%llu,\"drops\":%llu,\"learnedPorts\":%llu,\"learnedIPv4\":%llu,\"learnedDHCP\":%llu,\"arpOut\":%llu,\"arpIn\":%llu,\"dhcpOut\":%llu,\"dhcpIn\":%llu,\"ipv4Out\":%llu,\"ipv4In\":%llu,\"ignoredWrongBridge\":%llu,\"dhcpOption61Rewrite\":%llu,\"dhcpChecksumRewrite\":%llu,\"inboundNoVMMatch\":%llu,\"inboundNoLearnedPort\":%llu}",
					 written_sessions == 0 ? "" : ",", session_state,
					 session_message, snap[i].lab_id,
					 snap[i].switch_id, snap[i].bridge,
					 snap[i].uplink, snap[i].vm_count,
					 (unsigned long long)snap[i].stats.tx_to_uplink,
					 (unsigned long long)snap[i].stats.rx_to_vm,
					 (unsigned long long)snap[i].stats.drops,
					 (unsigned long long)snap[i].stats.learned_ports,
					 (unsigned long long)snap[i].stats.learned_ipv4,
					 (unsigned long long)snap[i].stats.learned_dhcp,
					 (unsigned long long)snap[i].stats.arp_out,
					 (unsigned long long)snap[i].stats.arp_in,
					 (unsigned long long)snap[i].stats.dhcp_out,
					 (unsigned long long)snap[i].stats.dhcp_in,
					 (unsigned long long)snap[i].stats.ipv4_out,
					 (unsigned long long)snap[i].stats.ipv4_in,
					 (unsigned long long)snap[i].stats.ignored_wrong_bridge,
					 (unsigned long long)snap[i].stats.dhcp_option61_rewrite,
					 (unsigned long long)snap[i].stats.dhcp_checksum_rewrite,
					 (unsigned long long)snap[i].stats.inbound_no_vm_match,
					 (unsigned long long)snap[i].stats.inbound_no_learned_port);
		written_sessions++;
	}
	if (len < FOXLAB_MACNAT_STATUS_SIZE)
		len += scnprintf(status + len, FOXLAB_MACNAT_STATUS_SIZE - len,
				 "],\"lastConfigBytes\":%zu}\n", config_len);
	if (len >= FOXLAB_MACNAT_STATUS_SIZE)
		len = FOXLAB_MACNAT_STATUS_SIZE - 1;
	ret = simple_read_from_buffer(buf, count, ppos, status, len);
	kfree(status);
	kfree(snap);
	return ret;
}

static ssize_t foxlab_macnat_write(struct file *file, const char __user *buf,
				   size_t count, loff_t *ppos)
{
	struct foxlab_pending_session pending;
	struct foxlab_clear_request clear;
	char *cmd;
	size_t len = min_t(size_t, count, FOXLAB_MACNAT_MAX_CONFIG - 1);
	int ret = 0;

	if (len == 0)
		return 0;
	cmd = memdup_user_nul(buf, len);
	if (IS_ERR(cmd))
		return PTR_ERR(cmd);

	mutex_lock(&config_lock);
	memcpy(last_config, cmd, len);
	last_config[len] = '\0';
	last_config_len = len;
	if (strncmp(cmd, "configure", 9) == 0) {
		ret = foxlab_parse_configure(cmd, &pending);
		if (!ret)
			ret = foxlab_configure_session(&pending);
	} else if (strncmp(cmd, "clear", 5) == 0) {
		ret = foxlab_parse_clear(cmd, &clear);
		if (!ret)
			foxlab_clear_sessions(&clear);
	} else {
		ret = -EINVAL;
	}
	mutex_unlock(&config_lock);
	kfree(cmd);

	if (ret)
		return ret;
	return count;
}

static const struct file_operations foxlab_macnat_fops = {
	.owner = THIS_MODULE,
	.read = foxlab_macnat_read,
	.write = foxlab_macnat_write,
	.llseek = no_llseek,
};

static struct miscdevice foxlab_macnat_miscdev = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = FOXLAB_MACNAT_DEVICE,
	.fops = &foxlab_macnat_fops,
	.mode = 0666,
};

int foxlab_register_control(void)
{
	return misc_register(&foxlab_macnat_miscdev);
}

void foxlab_unregister_control(void)
{
	misc_deregister(&foxlab_macnat_miscdev);
}
