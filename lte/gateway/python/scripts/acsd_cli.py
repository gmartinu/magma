#!/usr/bin/env python3

"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Manage the CPEs acsd serves in claimed mode (outside the Magma core):
add, remove and list claims, reset a CPE's credentials, and write a
self-signed cert for the WAN listener in development.

    acsd_cli.py claim-add --oui 00A1B2 --product-class Titan4000 --serial SN1
    acsd_cli.py claim-list
"""

from magma.acsd.cli import main

if __name__ == '__main__':
    main()
