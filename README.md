# sync-space
sync-space is a utility for detecting changes to a tree and updating a remote host. It can be used to maintain a code tree on a remote build box.

## Prerequisites

 * rsync
 * ssh
 
## Usage

To sync the current directory to a remote box under $HOME/my-project:

    $ sync-space user@my-dev-box.cloud.com:my-project .

Use Ctrl-C to disconnect.
