// 最小化的 chrome 扩展 API 声明：只覆盖本扩展实际用到的接口，不引入 @types/chrome。
declare namespace chrome {
  namespace storage {
    interface StorageArea {
      get(keys: string | string[]): Promise<Record<string, unknown>>;
      set(items: Record<string, unknown>): Promise<void>;
      remove(keys: string | string[]): Promise<void>;
    }
    const local: StorageArea;
    type ChangeListener = (
      changes: Record<string, { newValue?: unknown }>,
      area: string,
    ) => void;
    const onChanged: {
      addListener(listener: ChangeListener): void;
      removeListener(listener: ChangeListener): void;
    };
  }
  namespace permissions {
    interface Permissions {
      origins?: string[];
    }
    function request(permissions: Permissions): Promise<boolean>;
  }
  namespace tabs {
    interface Tab {
      id?: number;
    }
    function query(info: {
      active?: boolean;
      currentWindow?: boolean;
    }): Promise<Tab[]>;
  }
  namespace scripting {
    interface InjectionResult<T> {
      result?: T;
    }
    function executeScript<T>(injection: {
      target: { tabId: number };
      func: () => T;
    }): Promise<InjectionResult<T>[]>;
  }
}
