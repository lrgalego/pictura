# infra

Declarations shipyard converges onto the VM on every deploy: the compose
file, the tunnel ingress, and any systemd units under `systemd/`. Edit them
here — never on the VM; a deploy overwrites drift by design.

Standing the whole thing up (VM, firewall, registry, tunnel, DNS, bootstrap):

    shipyard provision

The full reasoning behind every piece lives in the shipyard repo's docs
(`docs/deploy-playbook.md`).
