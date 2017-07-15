# sync-ssh
sync-ssh gives you a remote shell while at the same time continuously syncing your local tree with the remote host.

This is useful for remote development. Your code stays local (for IDE indexing and the like) but your tree is also visible and kept up-to-date on the remote host. The remote host (typically larger/faster) can then be used for builds and test.

## Prerequisites

 * rsync
 * ssh
 * fswatch

## Usage

To sync the current directory to a remote box under $HOME:

    $ sync-ssh . user@host:

Exiting the remote shell also terminates the continuous sync.
