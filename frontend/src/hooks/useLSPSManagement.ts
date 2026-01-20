
import { useCallback, useState } from 'react';
import { request } from 'src/utils/request';

export interface LSP {
    name: string;
    pubkey: string;
    host: string;
    active: boolean;
    isCommunity?: boolean;
    isSystem?: boolean; // Added
    description?: string;
}

export function useLSPSManagement() {
    const [lsps, setLsps] = useState<LSP[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const [initialized, setInitialized] = useState(false);

    const fetchLSPs = useCallback(async () => {
        setLoading(true);
        try {
            const data = await request<LSP[]>('/api/lsps');
            // Map backend isSystem to isCommunity for UI compatibility
            const mapped = (data || []).map(l => ({
                ...l,
                isCommunity: l.isSystem || l.isCommunity
            }));
            setLsps(mapped);
            setError(null);
        } catch (e: any) {
            setError(e.message || 'Failed to fetch LSPs');
        } finally {
            setLoading(false);
            setInitialized(true);
        }
    }, []);

    const addLSP = useCallback(async (name: string, uri: string) => {
        try {
             // Basic parsing match for pubkey@host:port or just host:port?
             // Backend expects { name, pubkey, host }
             // UI sends URI: pubkey@host:port
             const parts = uri.split('@');
             if (parts.length !== 2) throw new Error("Invalid URI format");
             
             await request('/api/lsps', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ name, pubkey: parts[0], host: parts[1] }),
            });
            await fetchLSPs();
            return true;
        } catch (e: any) {
            throw new Error(e.message || 'Failed to add LSP');
        }
    }, [fetchLSPs]);

    const removeLSP = useCallback(async (pubkey: string) => {
        try {
            await request(`/api/lsps/${pubkey}`, {
                method: 'DELETE',
            });
            await fetchLSPs();
        } catch (e: any) {
            throw new Error(e.message || 'Failed to remove LSP');
        }
    }, [fetchLSPs]);

    const setActiveLSP = useCallback(async (pubkey: string) => {
        try {
            await request(`/api/lsps/${pubkey}`, {
                method: 'PUT',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ active: true }),
            });
            await fetchLSPs();
        } catch (e: any) {
            throw new Error(e.message || 'Failed to select LSP');
        }
    }, [fetchLSPs]);

    const deactivateLSP = useCallback(async (pubkey: string) => {
        try {
            await request(`/api/lsps/${pubkey}`, {
                method: 'PUT',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ active: false }),
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
                    promises.push(
                        request(`/api/lsps/${lsp.pubkey}`, { method: 'DELETE' })
                    );
                }
            }

            // 2. Handle Additions and Modifications
            const originalMap = new Map(original.map(l => [l.pubkey, l]));
            for (const lsp of current) {
                if (!originalMap.has(lsp.pubkey)) {
                    // New LSP
                    promises.push(
                        request('/api/lsps', {
                            method: 'POST',
                            headers: {
                                'Content-Type': 'application/json',
                            },
                            body: JSON.stringify({ 
                                name: lsp.name, 
                                pubkey: lsp.pubkey, 
                                host: lsp.host 
                            }),
                        })
                    );
                } else {
                    // 3. Handle Status Changes
                    const originalLSP = originalMap.get(lsp.pubkey)!;
                    if (originalLSP.active !== lsp.active) {
                        promises.push(
                            request(`/api/lsps/${lsp.pubkey}`, {
                                method: 'PUT',
                                headers: {
                                    'Content-Type': 'application/json',
                                },
                                body: JSON.stringify({ active: lsp.active }),
                            })
                        );
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
        initialized,
        error,
        fetchLSPs,
        addLSP,
        removeLSP,
        setActiveLSP,
        deactivateLSP,
        saveLSPChanges,
    };
}
