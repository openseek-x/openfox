// 最小化的 chrome 扩展 API 声明：只覆盖本扩展实际用到的接口，不引入 @types/chrome。
declare namespace chrome {
  namespace runtime {
    function getManifest(): { homepage_url: string };
  }
  namespace cookies {
    function get(details: {
      url: string;
      name: string;
    }): Promise<{ value: string } | null>;
  }
  namespace tabs {
    interface Tab {
      id?: number;
    }
    function create(info: { url: string }): Promise<Tab>;
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
