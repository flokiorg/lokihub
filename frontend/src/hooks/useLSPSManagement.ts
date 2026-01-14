
import { useCallback, useState } from 'react';
import { request } from 'src/utils/request';

export interface LSP {
    name: string;
    pubkey: string;
    host: string;
    active: boolean;
    isCommunity?: boolean;
    description?: string;
}

export function useLSPSManagement() {
    const [lsps, setLsps] = useState<LSP[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const fetchLSPs = useCallback(async () => {
        setLoading(true);
        try {
            const data = await request<LSP[]>('/api/lsps/all');
            // If null is returned (no LSPs yet), treat as empty array
            setLsps(data || []);
            setError(null);
        } catch (e: any) {
            setError(e.message || 'Failed to fetch LSPs');
        } finally {
            setLoading(false);
        }
    }, []);

    const addLSP = useCallback(async (name: string, uri: string) => {
        try {
            await request('/api/lsps/all', {
                method: 'POST',
                body: JSON.stringify({ name, uri }),
            });
            await fetchLSPs();
            return true;
        } catch (e: any) {
            throw new Error(e.message || 'Failed to add LSP');
        }
    }, [fetchLSPs]);

    const removeLSP = useCallback(async (pubkey: string) => {
        try {
            await request(`/api/lsps/all?pubkey=${pubkey}`, {
                method: 'DELETE',
            });
            await fetchLSPs();
        } catch (e: any) {
            throw new Error(e.message || 'Failed to remove LSP');
        }
    }, [fetchLSPs]);

    const setActiveLSP = useCallback(async (pubkey: string) => {
        try {
            await request('/api/lsps/selected', {
                method: 'POST',
                body: JSON.stringify({ pubkey }),
            });
            await fetchLSPs();
        } catch (e: any) {
            throw new Error(e.message || 'Failed to select LSP');
        }
    }, [fetchLSPs]);

    const deactivateLSP = useCallback(async (pubkey: string) => {
        try {
            await request(`/api/lsps/selected?pubkey=${pubkey}`, {
                method: 'DELETE',
            });
            await fetchLSPs();
        } catch (e: any) {
            throw new Error(e.message || 'Failed to deselect LSP');
        }
    }, [fetchLSPs]);

    const saveLSPChanges = useCallback(async (original: LSP[], current: LSP[]) => {
        try {
            const promises: Promise<any>[] = [];

            // 1. Handle Removals
            const currentPubkeys = new Set(current.map(l => l.pubkey));
            for (const lsp of original) {
                if (!currentPubkeys.has(lsp.pubkey)) {
                    // Remove LSP then Disconnect
                    promises.push(
                        request(`/api/lsps/all?pubkey=${lsp.pubkey}`, { method: 'DELETE' })
                        .then(() => request(`/api/peers/${lsp.pubkey}`, { method: 'DELETE' }))
                        .catch(e => console.warn(`Failed to disconnect ${lsp.name}:`, e))
                    );
                }
            }

            // 2. Handle Additions and Modifications
            const originalMap = new Map(original.map(l => [l.pubkey, l]));
            for (const lsp of current) {
                if (!originalMap.has(lsp.pubkey)) {
                    // New LSP
                    const uri = `${lsp.pubkey}@${lsp.host}`;
                    
                    promises.push((async () => {
                        // 1. Connect Peer (Fail if connection fails)
                        try {
                            await request('/api/peers', {
                                method: 'POST',
                                headers: { 'Content-Type': 'application/json' },
                                body: JSON.stringify({ 
                                    host: lsp.host, 
                                    pubkey: lsp.pubkey,
                                    perm: true 
                                })
                            });
                        } catch (e: any) {
                             throw new Error(`Failed to connect to ${lsp.name}: ${e.message}`);
                        }

                        // 2. Add LSP
                        await request('/api/lsps/all', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ name: lsp.name, uri }),
                        });

                        // 3. Activate if needed
                        if (lsp.active) {
                            await request('/api/lsps/selected', {
                                method: 'POST',
                                headers: { 'Content-Type': 'application/json' },
                                body: JSON.stringify({ pubkey: lsp.pubkey }),
                            });
                        }
                    })());
                } else {
                    // 3. Handle Status Changes
                    const originalLSP = originalMap.get(lsp.pubkey)!;
                    if (originalLSP.active !== lsp.active) {
                         if (lsp.active) {
                             // Enabling: Connect Peer first
                             promises.push((async () => {
                                 try {
                                     await request('/api/peers', {
                                         method: 'POST',
                                         headers: { 'Content-Type': 'application/json' },
                                         body: JSON.stringify({ 
                                             host: lsp.host, 
                                             pubkey: lsp.pubkey,
                                             perm: true 
                                         })
                                     });
                                 } catch (e: any) {
                                     throw new Error(`Failed to connect to ${lsp.name}: ${e.message}`);
                                 }

                                 await request('/api/lsps/selected', {
                                     method: 'POST',
                                     headers: { 'Content-Type': 'application/json' },
                                     body: JSON.stringify({ pubkey: lsp.pubkey }),
                                 });
                             })());
                         } else {
                             // Disabling: Deselect then Disconnect
                             promises.push((async () => {
                                 await request(`/api/lsps/selected?pubkey=${lsp.pubkey}`, {
                                     method: 'DELETE',
                                 });
                                 // Attempt disconnect, but don't fail hard if it fails
                                 try {
                                     await request(`/api/peers/${lsp.pubkey}`, { method: 'DELETE' });
                                 } catch (e) {
                                     console.warn("Failed to disconnect peer", e);
                                 }
                             })());
                         }
                    }
                }
            }

            await Promise.all(promises);
            await fetchLSPs();
            return true;
        } catch (e: any) {
             throw new Error(e.message || 'Failed to save LSP changes');
        }
    }, [fetchLSPs]);

    return {
        lsps,
        loading,
        error,
        fetchLSPs,
        addLSP,
        removeLSP,
        setActiveLSP,
        deactivateLSP,
        saveLSPChanges,
    };
}
