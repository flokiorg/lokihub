
export enum LinkStatus {
  SharedNode,
  ThisNode,
  OtherNode,
  Unlinked,
}

export function useLinkAccount() {
  return {
    loading: false,
    loadingLinkStatus: false,
    linkStatus: LinkStatus.Unlinked,
    linkAccount: async () => {},
  };
}
