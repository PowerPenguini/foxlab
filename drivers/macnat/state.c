// SPDX-License-Identifier: GPL-2.0
#include "foxlab_macnat.h"

DEFINE_SPINLOCK(session_lock);
struct foxlab_session sessions[FOXLAB_MACNAT_MAX_SESSIONS];
